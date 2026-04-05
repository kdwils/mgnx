# Section 3: BEP-05 DHT Crawler — Architecture & Implementation Guide

> **Scope**: BEP-05 DHT protocol only. BEP-09/10 metadata fetch is Section 4.
> **References**: https://www.bittorrent.org/beps/bep_0005.html (authoritative),
> BEP-51 (sample_infohashes), BEP-42 (node ID security), BEP-43 (read-only mode).

---

## Package Layout

```
dht/
  node.go         — Node struct: ID, addr, last_seen, failure_count
  bucket.go       — Bucket: min/max range, nodes[], replacement_cache[], last_changed
  table.go        — Routing table: adaptive bucket list, Insert, Closest, Refresh
  krpc.go         — KRPC message types with bencode struct tags
  server.go       — UDP socket, read/write loops, transaction map
  transaction.go  — Transaction: ID, addr, timeout, response channel
  token.go        — Rotating-secret token generation for get_peers responses
  bootstrap.go    — DNS resolution, self-lookup convergence
  crawler.go      — announce_peer listener + BEP-51 sample_infohashes traversal
  dedup.go        — Double-rotating bloom filter
```

All files are flat under `dht/` — no sub-packages. Follow the project convention: no `internal/`, no `pkg/`.

---

## Core Types and Interfaces

Define these first. Everything else is built around them.

### Public contract (crawler.go)

```go
type HarvestEvent struct {
    Infohash [20]byte
    SourceIP net.IP
    Port     int
    SeenAt   time.Time
}

type Crawler interface {
    Infohashes() <-chan HarvestEvent
    Start(ctx context.Context) error
    Stop()
}
```

`Infohashes()` is the only coupling point to Section 4 (metadata fetch). The channel is buffered (see Backpressure below).

### Node (node.go)

```go
type NodeID [20]byte

type Node struct {
    ID          NodeID
    Addr        *net.UDPAddr
    LastSeen    time.Time
    FailureCount int
}
```

Node classification (not stored as a field — computed):
- **Good**: responded to a query within the last 15 minutes, OR sent us a query after having responded previously.
- **Questionable**: no contact for 15+ minutes, not confirmed bad.
- **Bad**: 2+ consecutive query failures.

The routing table only sends `ping` to questionable nodes before evicting them (ping-before-evict).

### Bucket (bucket.go)

```go
type Bucket struct {
    Min             NodeID        // inclusive lower bound
    Max             NodeID        // exclusive upper bound
    Nodes           []*Node       // up to k=8, ordered by last_seen ascending
    ReplacementCache []*Node      // overflow queue, up to k=8
    LastChanged     time.Time
}
```

> **Note**: use `NodeID` (`[20]byte`) rather than `big.Int` for bucket boundaries. `big.Int` allocates on every comparison; a byte-slice comparison (`bytes.Compare`) is allocation-free and sufficient for all range checks (`Contains`, `Split`).

A bucket is "stale" when `time.Since(LastChanged) > 15 * time.Minute`. Stale buckets trigger a refresh lookup.

### KRPC Messages (krpc.go)

Define one struct per message direction. Use `bencode` struct tags (use `github.com/anacrolix/torrent/bencode` — do not hand-roll bencode).

```go
// Top-level envelope for all messages
type Msg struct {
    T string   `bencode:"t"`           // transaction ID
    Y string   `bencode:"y"`           // "q", "r", or "e"
    Q string   `bencode:"q,omitempty"` // query name (queries only)
    A *MsgArgs `bencode:"a,omitempty"` // query args
    R *Return  `bencode:"r,omitempty"` // response body
    E []any    `bencode:"e,omitempty"` // error: [code int, msg string]
    V string   `bencode:"v,omitempty"` // client version
}

// Arguments for all query types (fields are union across types)
type MsgArgs struct {
    ID       string `bencode:"id"`                   // always required
    Target   string `bencode:"target,omitempty"`     // find_node, sample_infohashes
    InfoHash string `bencode:"info_hash,omitempty"`  // get_peers, announce_peer
    Token    string `bencode:"token,omitempty"`      // announce_peer
    Port        int  `bencode:"port,omitempty"`       // announce_peer
    ImpliedPort *int `bencode:"implied_port,omitempty"` // announce_peer; *int so omitempty distinguishes "absent" from "0" (false)
}

// Response body for all query types
type Return struct {
    ID      string   `bencode:"id"`               // always present
    Nodes   string   `bencode:"nodes,omitempty"`   // compact node list (26 bytes each)
    Values  []string `bencode:"values,omitempty"`  // compact peer list (6 bytes each)
    Token   string   `bencode:"token,omitempty"`   // get_peers response
    Samples string   `bencode:"samples,omitempty"` // BEP-51: raw 20-byte infohashes
    Interval int     `bencode:"interval,omitempty"`// BEP-51: seconds until re-query
    Num     int      `bencode:"num,omitempty"`     // BEP-51: total stored infohashes
}
```

Compact encodings to implement as helper functions:
- **Node** (IPv4): 26 bytes — `[20]byte ID || [4]byte IP (big-endian) || [2]byte port (big-endian)`
- **Peer** (IPv4): 6 bytes — `[4]byte IP || [2]byte port`
- Parse `nodes` string as consecutive 26-byte chunks: `len(nodes)/26` nodes.
- Parse `values` as a list of 6-byte strings.

---

## Implementation Order

Implement files in this sequence. Each step has a clear completion criterion.

### Step 1 — `node.go` and `bucket.go`

Pure data structures with no I/O. Implement:
- `NodeID` type with `XOR(other NodeID) NodeID` and `CommonPrefixLen(other NodeID) int` methods (needed for routing table distance comparisons).
- `Node` with `IsGood() bool`, `IsQuestionable() bool`, `IsBad() bool` computed from `LastSeen` and `FailureCount`.
- `Bucket` with `Contains(id NodeID) bool` (checks if `id` falls within `[Min, Max)`), `IsFull() bool` (`len(Nodes) == 8`), `IsStale() bool`.

**Done when**: unit tests for XOR distance, bucket range containment, and node classification pass.

### Step 2 — `table.go`

The routing table manages a sorted slice of `*Bucket`. Key operations:

- `Insert(node *Node)`: find the bucket containing `node.ID`. If not full, add. If full and bucket contains our ID, split it (create two half-range buckets, redistribute nodes). If full and doesn't contain our ID, apply ping-before-evict: ping the LRS (least recently seen) node; if no response after timeout, evict it and insert the new node.
- `Closest(target NodeID, n int) []*Node`: return the `n` nodes closest to `target` by XOR distance across all buckets. Collect all good+questionable nodes, sort by `XOR(target, node.ID)`, return top `n`.
- `MarkSuccess(id NodeID)`: update `LastSeen`, reset `FailureCount` to 0.
- `MarkFailure(id NodeID)`: increment `FailureCount`. If `FailureCount >= 2`, the node is bad and should be evicted if a replacement exists.
- `StaleBootstrap() []*Bucket`: return buckets where `IsStale()` is true, for the refresh scheduler.

**Routing table persistence**: on `Stop()`, serialize all good nodes as consecutive 26-byte compact records to the path in `cfg.DHT.NodesPath` (default `$HOME/.mgnx/dht_nodes.dat`) using an atomic write (write to `.tmp`, rename). On `Start()`, load and seed into the table before running `find_node` on each. Use the config field — do not hardcode the path.

**Done when**: table correctly splits buckets around own ID, closest-node lookup returns correct XOR-sorted results, persistence round-trips cleanly.

### Step 3 — `transaction.go`

```go
type Transaction struct {
    ID       string
    Addr     *net.UDPAddr
    SentAt   time.Time
    Response chan *Msg   // capacity 1; closed on timeout
}
```

The transaction map lives on the server:
- `map[string]*Transaction` guarded by a `sync.RWMutex`
- Transaction IDs: 2-byte big-endian counter (`atomic.Uint32`, masked to 16 bits), stored as a raw 2-byte binary string (not hex or decimal — bencode strings are byte strings). When creating a new transaction, skip any ID already present in the map to avoid the rare but possible wrap-around collision.
- A single goroutine sweeps the map every 1 second for entries older than 10 seconds, closes their `Response` channel, increments the target node's failure count, and deletes them

**Done when**: sweeper correctly times out in-flight transactions and the node's failure count is updated.

### Step 4 — `token.go`

Implement the rotating-secret token scheme:

- Maintain `currentSecret` and `previousSecret` (random 8-byte values)
- Rotate every 5 minutes: `previousSecret = currentSecret`, `currentSecret = newRandom()`
- `Generate(ip net.IP) string`: returns `SHA1(ip || currentSecret)[:4]` as a raw byte string
- `Validate(ip net.IP, token string) bool`: returns true if token matches `SHA1(ip || currentSecret)` or `SHA1(ip || previousSecret)`

For a pure crawler, `Validate` is only needed if you're enforcing tokens on inbound `announce_peer` (optional — just ACK without validation if you're only harvesting infohashes).

**Done when**: token round-trips correctly through a 5-minute rotation cycle.

### Step 5 — `krpc.go`

Implement compact encoding/decoding helpers:

```go
func EncodeNodes(nodes []*Node) string        // returns concatenated 26-byte records
func DecodeNodes(s string) ([]*Node, error)   // parses 26-byte chunks
func EncodePeer(ip net.IP, port int) string   // 6-byte record
func DecodePeers(values []string) ([]net.Addr, error)
func ParseNodeID(s string) (NodeID, error)    // validates exactly 20 bytes
```

Also implement the error code constants:
```go
const (
    ErrGeneric  = 201
    ErrServer   = 202
    ErrProtocol = 203
    ErrMethod   = 204
)
```

**Done when**: round-trip encode/decode tests pass for nodes and peers with known byte sequences.

### Step 6 — `server.go`

The UDP server is the heart of the DHT node. Structure:

```go
type Server struct {
    conn        *net.UDPConn
    ourID       NodeID
    table       *RoutingTable
    txns        map[string]*Transaction
    txnsMu      sync.RWMutex
    txnCounter  atomic.Uint32
    outbound    chan *outMsg      // buffered, capacity 512
    harvest     chan HarvestEvent // output channel, buffered 10000
    token       *TokenManager
    dedup       *BloomFilter
    rate        *rate.Limiter    // golang.org/x/time/rate
    // ... config fields
}

type outMsg struct {
    addr *net.UDPAddr
    msg  *Msg
}
```

**Server initialization**: set `SO_RCVBUF` to 4 MB on the UDP socket immediately after binding. The default kernel buffer (~208–256 KB) will drop datagrams during bursts before the read loop can drain them. Use `SyscallConn().Control()` to reach the raw fd — there is no higher-level Go API for this:
```go
rawConn, _ := conn.SyscallConn()
rawConn.Control(func(fd uintptr) {
    syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4<<20)
})
```
If the OS limit is lower than 4 MB, the kernel silently clamps the value — the socket remains functional with a smaller buffer.

**Read loop** (1 goroutine):
1. `conn.ReadFromUDP(buf)` with a 2048-byte buffer obtained from a `sync.Pool`
2. **Copy the datagram bytes before returning the buffer to the pool.** The decoded `*Msg` and any byte slices within it must not reference the pooled buffer. Decode into a fresh allocation or copy the raw bytes (`data := make([]byte, n); copy(data, buf[:n])`) before decoding.
3. Bencode-decode into `*Msg`
4. If `y == "r"` or `y == "e"`: look up transaction map by `t`, send to `Response` channel, update routing table (`MarkSuccess`)
5. If `y == "q"`: dispatch to query handler via non-blocking send to the handler channel (capacity 512); drop and increment a metric if full
6. Unknown `y`: ignore silently

**Query handler pool** (4–8 goroutines reading from the handler channel, buffered at 512):
- `"ping"`: respond with `{"y":"r","r":{"id":"<our id>"}}`
- `"find_node"`: call `table.Closest(target, 8)`, respond with encoded nodes
- `"get_peers"`: call `table.Closest(infohash, 8)`, respond with nodes + token; if we had peers we'd return `values` but as a crawler we always return nodes
- `"announce_peer"`: extract `info_hash` from `a`, send to harvest pipeline (see Step 8), respond with ACK
- `"sample_infohashes"`: add returned `samples` to harvest pipeline, add `nodes` to routing table
- Unknown query: treat as `find_node` for any provided `target` or `info_hash` (forward-compat)

**Write loop** (1 goroutine):
1. Read from `outbound` channel
2. Acquire token from rate limiter (`rate.Limiter.Wait(ctx)`)
3. Bencode-encode the message
4. `conn.WriteToUDP(encoded, addr)`

**Query sender** (used by bootstrap and BEP-51 traversal):
```go
func (s *Server) Query(ctx context.Context, addr *net.UDPAddr, msg *Msg) (*Msg, error)
```
Creates a `Transaction`, enqueues to `outbound`, waits on `Transaction.Response` channel with `ctx` timeout.

**Done when**: the server can send/receive ping round-trips with a known DHT node.

### Step 7 — `bootstrap.go`

Bootstrap is a one-time convergence process re-run on startup (and optionally on a slow periodic timer to refresh distant buckets).

```go
func Bootstrap(ctx context.Context, s *Server, bootstrapAddrs []string) error
```

Algorithm:
1. DNS-resolve each bootstrap hostname (accept multiple A records per name)
2. Send `find_node` to each resolved address with `target = s.ourID`
3. Add returned nodes to the routing table
4. Build a priority queue of returned nodes sorted by `XOR(node.ID, s.ourID)` ascending
5. Send `find_node` to the `alpha=3` closest un-queried nodes
6. Repeat until a full round produces no node closer than the current best
7. Trigger a bucket refresh for any bucket still stale after convergence

**Node ID generation**:
1. On first start: generate a random `[20]byte` ID, save to `cfg.DHT.NodeIDPath` (default `$HOME/.mgnx/dht_id`)
2. After receiving the first bootstrap response, extract the `ip` extension field (the responder's view of our external IP)
3. Regenerate a BEP-42 compliant ID using that IP and save it (overwriting the random one)
4. On subsequent starts: load the saved ID directly

BEP-42 ID derivation (IPv4 only — BEP-32/IPv6 is out of scope):
```
r         = node_id[19] & 0x07   // last 3 bits of last byte
masked_ip = ip_uint32 & 0x030f3fff  // IPv4 mask; IPv6 uses a different mask per BEP-42 §3
crc       = crc32c(masked_ip | (r << 29))
// node_id[0..2] top 21 bits must equal crc top 21 bits
// node_id[19] = r
// node_id[3..18] = random
```

> **Note**: BEP-42 defines separate IP masks for IPv4 (`0x030f3fff`) and IPv6. Since this implementation targets IPv4 only (BEP-32 skipped), only the IPv4 mask applies. If IPv6 support is added later, revisit this derivation.

Use CRC32C via `hash/crc32` with the Castagnoli polynomial (`crc32.MakeTable(crc32.Castagnoli)`) — no extra dependency needed.

**Done when**: after `Bootstrap()` returns, `table.Closest(s.ourID, 8)` returns 8 good nodes.

### Step 8 — `dedup.go`

Double-rotating bloom filter:

```go
type BloomFilter struct {
    active   *bloom.BloomFilter  // current window
    previous *bloom.BloomFilter  // previous window
    mu       sync.Mutex
    rotateAt time.Time
}

func (b *BloomFilter) SeenOrAdd(h [20]byte) bool
// Returns true if h was already seen (in either filter).
// If not seen: adds to active filter, returns false.
// The entire body runs under b.mu — acquire the lock before checking rotateAt,
// rotating filters, and testing/adding to the bloom filters. Do not check
// rotateAt outside the lock; that is a TOCTOU race.
```

Parameters:
- `n = 1_000_000`, `p = 0.001` → ~1.8 MB per filter
- Rotate every 12 hours
- Use `github.com/bits-and-blooms/bloom/v3` (well-maintained, standard interface)

**Done when**: a known infohash returns `true` on second call, and false-positive rate is empirically ~0.1%.

### Step 9 — `crawler.go`

The `crawlerImpl` struct wraps the `Server` and drives two harvest modes.

```go
type crawlerImpl struct {
    server  *Server
    harvest chan HarvestEvent   // same channel as server.harvest
    dedup   *BloomFilter
    // config: bep51Workers int, etc.
}
```

**Passive listener** (already wired in step 6 — `announce_peer` query handler):
```
query handler goroutine receives announce_peer
  → extract a.info_hash (20 bytes)
  → if dedup.SeenOrAdd(h): return (already known)
  → non-blocking send to harvest channel:
      select { case s.harvest <- event: default: /* drop, increment metric */ }
  → send ACK response
```

**BEP-51 active traversal** (1–4 goroutines in `crawler.go`):
```
goroutine bep51Worker():
    queue = priority queue of (node, target) pairs, ordered by XOR to target
    seen  = map[NodeID]time.Time  // tracks when we last queried each node

    seed initial targets: send sample_infohashes to table.Closest(randomTarget, 8)
    loop:
        pick next (node, target) from queue not in seen or past interval
        resp = server.Query(ctx, node.Addr, sample_infohashes{target})
        for each 20-byte hash in resp.Samples:
            if !dedup.SeenOrAdd(h): send to harvest channel
        for each node in resp.Nodes:
            table.Insert(node)
            queue.Push(node, target)  // use returned nodes for next hop
        seen[node.ID] = now + resp.Interval seconds
        // randomize target periodically to cover full keyspace
```

Run `bep51Workers` (configurable, default 2) of these goroutines.

**Harvest channel backpressure** — the channel is buffered at 10,000. Use non-blocking sends everywhere in the harvest path. Track dropped count as a metric (hook into the metrics system added in Section 10).

**Done when**: running for 60 seconds, the crawler emits at least a few hundred unique infohashes from a live network.

---

## Concurrency Map

```
UDP Socket (single *net.UDPConn)
    │
    ├── Read Loop goroutine (1)
    │       reads raw datagrams
    │       decodes bencode
    │       routes: response → txn map | query → handler channel
    │
    ├── Query Handler goroutines (4–8)
    │       reads from handler channel
    │       calls routing table operations
    │       sends responses via outbound channel
    │       on announce_peer: → dedup → harvest channel
    │
    ├── Write Loop goroutine (1)
    │       reads from outbound channel
    │       waits on rate limiter token
    │       encodes and writes UDP
    │
    ├── Transaction Sweeper goroutine (1)
    │       ticks every 1 second
    │       removes expired transactions (> 10s)
    │       marks node failure on timeout
    │
    ├── Bucket Refresh goroutine (1)
    │       ticks every 1 minute
    │       finds stale buckets (> 15 min since LastChanged)
    │       fires find_node for random ID in stale range
    │
    └── BEP-51 Traversal goroutines (1–4)
            drives sample_infohashes traversal
            respects per-node interval
            feeds harvest channel
```

Total: ~12–20 goroutines. All goroutines respect `context.Context` cancellation and exit cleanly on `Stop()`.

---

## Configuration

Add to the existing `Config` struct (config package):

```go
DHT struct {
    Port           int           `mapstructure:"port"`            // default 6881
    BootstrapNodes []string      `mapstructure:"bootstrap_nodes"` // override defaults
    RateLimit      float64       `mapstructure:"rate_limit"`      // queries/sec, default 25
    RateBurst      int           `mapstructure:"rate_burst"`      // default 25
    Workers        int           `mapstructure:"workers"`         // query handler goroutines, default 4
    BEP51Workers   int           `mapstructure:"bep51_workers"`   // default 2
    HarvestBuffer  int           `mapstructure:"harvest_buffer"`  // channel size, default 10000
    NodeIDPath     string        `mapstructure:"node_id_path"`    // default $HOME/.mgnx/dht_id
    NodesPath      string        `mapstructure:"nodes_path"`      // default $HOME/.mgnx/dht_nodes.dat
}
```

Env prefix: `mgnx_DHT_PORT`, `mgnx_DHT_RATE_LIMIT`, etc.

---

## Key Constants and Parameters

| Parameter | Value | Notes |
|---|---|---|
| Bucket size k | 8 | BEP-05 |
| Parallel queries alpha | 3 | Kademlia paper |
| Good-node window | 15 minutes | BEP-05 |
| Questionable → bad threshold | 2 failures | BEP-05 |
| Token secret rotation | 5 minutes | BEP-05 |
| Token validity window | 10 minutes | BEP-05 |
| Transaction timeout | 10 seconds | practical |
| Transaction sweeper interval | 1 second | |
| Bucket refresh check interval | 1 minute | |
| Stale bucket threshold | 15 minutes | BEP-05 |
| Rate limit default | 25 q/s, burst 25 | anacrolix default |
| Max concurrent transactions | 128–512 | practical |
| UDP read buffer | 2,048 bytes | MTU-safe |
| Socket receive buffer | 4 MB | `SO_RCVBUF` |
| Harvest channel buffer | 10,000 | |
| Bloom filter n | 1,000,000 | |
| Bloom filter p | 0.001 (0.1%) | |
| Bloom filter rotation | 12 hours | |
| BEP-51 max interval | 21,600 seconds | BEP-51 |
| Bootstrap nodes | router.bittorrent.com:6881, router.utorrent.com:6881, dht.transmissionbt.com:6881 | |
| Node ID size | 20 bytes / 160 bits | |
| Compact node (IPv4) | 26 bytes | 20 + 4 + 2 |
| Compact peer (IPv4) | 6 bytes | 4 + 2 |

---

## Dependencies to Add

```bash
go get github.com/anacrolix/torrent/bencode   # bencode encode/decode
go get github.com/bits-and-blooms/bloom/v3    # bloom filter
go get golang.org/x/time/rate                 # token bucket rate limiter
```

Do not add a full DHT library (anacrolix/dht, shiyanhui/dht, etc.) — the DHT logic is being built from scratch per the project charter. Only the above utility libraries are acceptable.

---

## Wire Between Sections

```
[Section 3: DHT Crawler]
    Crawler.Infohashes() <-chan HarvestEvent
                │
                ▼
[Section 4: BEP-09/10 Metadata Fetch]
    receives HarvestEvent.Infohash
    opens TCP connections to peers
    fetches info dict, validates against infohash
    writes PENDING torrent to Postgres
```

Section 3 has no knowledge of Postgres, classification, or enrichment. Its only output is `HarvestEvent` on the channel. Section 4 owns the TCP metadata fetch and the DB write.

---

## BEP Reference Map

| BEP | Title | Relevance |
|---|---|---|
| BEP-05 | DHT Protocol | Core — routing table, KRPC, all message types |
| BEP-42 | DHT Security Extension | Node ID derivation from public IP |
| BEP-43 | Read-Only DHT Nodes | Do NOT use — reduces announce_peer visibility |
| BEP-51 | DHT Infohash Indexing | Active harvesting via sample_infohashes |
| BEP-32 | IPv6 Extension for DHT | Optional v2 enhancement, skip for now |
| BEP-33 | DHT Scrape | Optional enhancement, skip for now |

All BEPs are available at https://www.bittorrent.org/beps/bep_0000.html
