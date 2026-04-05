# Section 4: BEP-09/10 Metadata Fetch & Ingest Pipeline (Steps 7–9)

## Overview

- **Step 7**: `metadata/` package — BEP-09/10 TCP metadata fetch (torrent name/files from a peer)
- **Step 8**: DB-level dedup in the ingest worker (bloom filter already lives in the DHT layer)
- **Step 9**: SQL queries, ingest worker, config addition, `serve.go` wiring

---

## Action 1 — Create `metadata/fetch.go`

No dependencies. Package `metadata`, file at `metadata/fetch.go`.

### Exported types

```go
type FileInfo struct {
    Path string
    Size int64
}

type TorrentInfo struct {
    Name      string
    Files     []FileInfo // always populated; single-file torrents get one entry
    TotalSize int64
}
```

### Exported function

```go
func Fetch(ctx context.Context, infohash [20]byte, addr net.TCPAddr) (*TorrentInfo, error)
```

Dial with `net.Dialer{}.DialContext(ctx, "tcp4", addr.String())`. Set `conn.SetDeadline` to the context deadline, or `time.Now().Add(15s)` if the context has no deadline. Wrap the conn with `bufio.NewReadWriter`.

### Wire protocol

**BitTorrent handshake — send (68 bytes):**

| Offset | Length | Value |
|--------|--------|-------|
| 0 | 1 | `0x13` (pstrlen = 19) |
| 1 | 19 | `"BitTorrent protocol"` |
| 20 | 8 | reserved bytes; set `reserved[5] \|= 0x10` for extension protocol |
| 28 | 20 | infohash |
| 48 | 20 | random peer_id |

**BitTorrent handshake — receive (68 bytes):**

Read 68 bytes. Validate `pstrlen == 19` and `pstr == "BitTorrent protocol"`. Return error if `peerReserved[5] & 0x10 == 0` (peer does not support extensions) or `peerInfohash != infohash`.

**Message framing (all messages after handshake):**

```
[4-byte big-endian length][1-byte msgType][payload...]
```

Keepalive: length = 0, no following bytes.

**Extension messages:** `msgType = 20`. First byte of payload is `extID`, remainder is bencode content.

### Extension handshake

Send `msgType=20, extID=0`, bencode payload:
```
{m: {ut_metadata: 1}, reqq: 250}
```

Receive: loop reading messages, skip non-20 and skip ext_id != 0. Decode first matching message as:

```go
type extHandshakeMsg struct {
    M            map[string]int `bencode:"m"`
    MetadataSize int            `bencode:"metadata_size"`
}
```

- `peerUTMetadataID = msg.M["ut_metadata"]` — error if 0
- `totalSize = msg.MetadataSize` — error if `<= 0` or `> 10 MB`

### Metadata piece fetch

`pieceSize = 16384`. `numPieces = ceil(totalSize / pieceSize)`.

For each piece index `i = 0..numPieces-1`:

Send `msgType=20, extID=peerUTMetadataID`, bencode payload:
```
{msg_type: 0, piece: i}
```

Receive: loop reading messages, skip non-20 and skip ext_id == 0 (re-handshake). For each candidate, wrap `payload[1:]` in a `bytes.Reader`, decode bencode header:

```go
type utMetadataMsgHeader struct {
    MsgType   int `bencode:"msg_type"`
    Piece     int `bencode:"piece"`
    TotalSize int `bencode:"total_size"`
}
```

Skip if `hdr.MsgType != 1` (not data) or `hdr.Piece != i`. Read remaining bytes from the `bytes.Reader` with `io.ReadAll` — these are the raw piece bytes. Append to assembled buffer.

### Validation and decode

After all pieces: `sha1.Sum(assembled)` must equal `infohash`, else return error.

Decode the assembled bytes:

```go
type rawFile struct {
    Path   []string `bencode:"path"`
    Length int64    `bencode:"length"`
}

type rawInfo struct {
    Name        string    `bencode:"name"`
    Length      int64     `bencode:"length"`
    Files       []rawFile `bencode:"files"`
    PieceLength int64     `bencode:"piece length"`
}
```

- **Single-file** (`len(rawInfo.Files) == 0`): `TotalSize = rawInfo.Length`, `Files = []FileInfo{{Path: rawInfo.Name, Size: rawInfo.Length}}`
- **Multi-file**: `TotalSize = sum of all file lengths`, each file's `Path = strings.Join(f.Path, "/")`

### Helper functions (unexported)

```go
func sendHandshake(w io.Writer, infohash [20]byte, peerID [20]byte) error
func readHandshake(r io.Reader) (reserved [8]byte, infohash [20]byte, err error)
func sendMsg(w io.Writer, msgType byte, extID byte, payload []byte) error
func readMsg(r io.Reader) (msgType byte, payload []byte, err error)
func decodeInfo(data []byte) (*TorrentInfo, error)
```

`sendMsg` writes `4-byte length (= 1+1+len(payload)) + msgType + extID + payload`, then flushes if `w` is a `*bufio.Writer`.

`readMsg` reads 4-byte length, reads full body, returns `body[0]` as msgType and `body[1:]` as payload. Length 0 → keepalive, returns `msgType=0, nil, nil`.

---

## Action 2 — Create `metadata/fetch_test.go`

No dependencies. Test `decodeInfo` only (no network required).

**`TestDecodeInfoSingleFile`**: bencode-marshal a dict with `name`, `length`, `piece length`, `pieces`. Call `decodeInfo`. Assert `Name`, `TotalSize`, `len(Files) == 1`, `Files[0].Path == name`, `Files[0].Size == length`.

**`TestDecodeInfoMultiFile`**: bencode-marshal a dict with `name`, `piece length`, `pieces`, and a `files` array with two entries (one nested path). Call `decodeInfo`. Assert `Name`, `TotalSize == sum`, `len(Files) == 2`, paths joined with `/`.

---

## Action 3 — Fix `sqlc.yaml`

Edit `sqlc.yaml`. Change:

```yaml
        package: "queries"
        out: "db/queries"
```

To:

```yaml
        package: "gen"
        out: "db/gen"
```

---

## Action 4 — Create `db/schema/queries/ingest.sql`

```sql
-- name: UpsertTorrentPending :execresult
INSERT INTO torrents (infohash, name, total_size, file_count)
VALUES (
    sqlc.arg('infohash'),
    sqlc.arg('name'),
    sqlc.arg('total_size'),
    sqlc.arg('file_count')
)
ON CONFLICT (infohash) DO NOTHING;

-- name: InsertTorrentFile :exec
INSERT INTO torrent_files (infohash, path, size, extension, is_video)
VALUES (
    sqlc.arg('infohash'),
    sqlc.arg('path'),
    sqlc.arg('size'),
    sqlc.narg('extension'),
    sqlc.arg('is_video')
);
```

`:execresult` on `UpsertTorrentPending` so the caller can inspect `tag.RowsAffected()` — 0 means the conflict branch fired and file inserts should be skipped.

`extension` uses `sqlc.narg` (nullable) → generates `pgtype.Text`. All others use `sqlc.arg`.

---

## Action 5 — Run `sqlc generate`

**Requires Actions 3 and 4.**

```bash
cd /Users/kylewilson/go-work/src/github.com/kdwils/mgnx && sqlc generate
```

Creates `db/gen/ingest.sql.go` containing:

- `UpsertTorrentPendingParams{Infohash string, Name string, TotalSize int64, FileCount int64}`
- `InsertTorrentFileParams{Infohash string, Path string, Size int64, Extension pgtype.Text, IsVideo bool}`
- `(q *Queries) UpsertTorrentPending(ctx, arg) (pgconn.CommandTag, error)`
- `(q *Queries) InsertTorrentFile(ctx, arg) error`
- Both methods added to the `Querier` interface in `db/gen/querier.go`

Do not proceed to Action 7 until this exits with code 0 and `db/gen/ingest.sql.go` exists.

---

## Action 6 — Edit `config/config.go`

Add `Ingest Ingest` to `Config` and add the new struct. `time` is already imported.

```go
type Config struct {
    Database Database `mapstructure:"database"`
    Server   Server   `mapstructure:"server"`
    DHT      DHT      `mapstructure:"dht"`
    Ingest   Ingest   `mapstructure:"ingest"`
}

type Ingest struct {
    Workers      int           `mapstructure:"workers"`
    FetchTimeout time.Duration `mapstructure:"fetch_timeout"`
}
```

Place `Ingest` struct immediately after the closing brace of the `DHT` struct.

---

## Action 7 — Create `ingest/worker.go`

**Requires Actions 5 and 6.**

Package `ingest`, file at `ingest/worker.go`.

```go
type Worker struct {
    crawler dht.Crawler
    queries gen.Querier
    cfg     config.Ingest
}

func New(crawler dht.Crawler, queries gen.Querier, cfg config.Ingest) *Worker
```

### `Run(ctx context.Context)`

Semaphore-bounded goroutine pool. `workers` defaults to 50 if `cfg.Workers <= 0`.

```go
sem := make(chan struct{}, workers)
var wg sync.WaitGroup
for {
    select {
    case ev, ok := <-w.crawler.Infohashes():
        if !ok { wg.Wait(); return }
        sem <- struct{}{}
        wg.Add(1)
        go func(e dht.HarvestEvent) {
            defer wg.Done()
            defer func() { <-sem }()
            w.process(ctx, e)
        }(ev)
    case <-ctx.Done():
        wg.Wait()
        return
    }
}
```

### `process(ctx, ev)`

1. `infohashHex = hex.EncodeToString(ev.Infohash[:])`
2. Call `w.queries.GetTorrentByInfohash(ctx, infohashHex)`:
   - `err == nil` → already exists, return
   - `!errors.Is(err, pgx.ErrNoRows)` → DB error, return
3. `fetchTimeout = cfg.FetchTimeout`; if `<= 0`, use `15 * time.Second`
4. `fetchCtx, cancel = context.WithTimeout(ctx, fetchTimeout)` + `defer cancel()`
5. `addr = net.TCPAddr{IP: ev.SourceIP, Port: ev.Port}`
6. `info, err = metadata.Fetch(fetchCtx, ev.Infohash, addr)` — return on error (silent drop)
7. `tag, err = w.queries.UpsertTorrentPending(ctx, ...)` — return on error
8. `if tag.RowsAffected() == 0 { return }` — race: another goroutine inserted first
9. For each `f` in `info.Files`: call `w.queries.InsertTorrentFile(ctx, ...)` (errors ignored)

### `isVideoExt(ext string) bool`

Returns true for: `.mkv`, `.mp4`, `.avi`, `.mov`, `.wmv`, `.m4v`, `.ts`, `.m2ts`, `.vob`, `.flv`, `.webm`.

---

## Action 8 — Edit `cmd/serve.go`

**Requires Actions 6 and 7.**

Replace the full file with:

```go
package cmd

import (
    "fmt"
    "log"

    "github.com/kdwils/mgnx/config"
    "github.com/kdwils/mgnx/db"
    "github.com/kdwils/mgnx/db/gen"
    "github.com/kdwils/mgnx/dht"
    "github.com/kdwils/mgnx/ingest"
    "github.com/kdwils/mgnx/logger"
    "github.com/kdwils/mgnx/server"
    "github.com/kdwils/mgnx/service"
    "github.com/spf13/cobra"
    "github.com/spf13/viper"
)

var serveCmd = &cobra.Command{
    Use:   "serve",
    Short: "Start the mgnx server",
    RunE: func(cmd *cobra.Command, args []string) error {
        cfg, err := config.New(viper.GetViper())
        if err != nil {
            log.Fatal(err)
        }

        l := logger.New(cfg.Server.LogLevel)

        ctx := cmd.Context()

        pool, err := db.Connect(ctx, cfg.Database.DSN())
        if err != nil {
            return fmt.Errorf("db connect: %w", err)
        }
        defer pool.Close()

        if err := db.RunMigrations(pool); err != nil {
            return fmt.Errorf("migrate: %w", err)
        }

        queries := gen.New(pool)

        crawler, err := dht.NewCrawler(cfg.DHT)
        if err != nil {
            return fmt.Errorf("dht crawler: %w", err)
        }

        if err := crawler.Start(ctx); err != nil {
            return fmt.Errorf("crawler start: %w", err)
        }
        defer crawler.Stop()

        ingestWorker := ingest.New(crawler, queries, cfg.Ingest)
        go ingestWorker.Run(ctx)

        svc := service.New(queries, cfg)
        srv := server.New(cfg.Server.Port, l, svc)
        return srv.Serve(ctx)
    },
}

func init() {
    rootCmd.AddCommand(serveCmd)
}
```

Key changes from current `serve.go`:
- Uses `cmd.Context()` instead of `context.Background()`
- Adds `dht.NewCrawler` + `crawler.Start` + `defer crawler.Stop()`
- Adds `ingest.New` + `go ingestWorker.Run(ctx)`
- Removes `context` import (no longer needed directly)

---

## Action 9 — Verify build

**Requires all prior actions.**

```bash
cd /Users/kylewilson/go-work/src/github.com/kdwils/mgnx && go build ./...
```

Must exit code 0 with no output. If it fails, fix only the file named in the compiler error.

---

## Action 10 — Run metadata tests

**Requires Action 9.**

```bash
cd /Users/kylewilson/go-work/src/github.com/kdwils/mgnx && go test ./metadata/...
```

Both `TestDecodeInfoSingleFile` and `TestDecodeInfoMultiFile` must pass.

---

## Ordering Constraints

```
Actions 1, 2, 3, 4, 6  — no dependencies, can run in parallel
Action 5               — requires 3 AND 4
Action 7               — requires 5 AND 6
Action 8               — requires 6 AND 7
Action 9               — requires 1, 2, 5, 6, 7, 8
Action 10              — requires 9
```

---

## Non-Obvious Implementation Notes

**`sendMsg` always writes two header bytes (msgType + extID).** The length prefix encodes `1 + 1 + len(payload)`. This function is only called for extension messages (type 20) in this codebase, so the two-byte header is always correct.

**`readMsg` returns `payload[1:]`.** After reading `length` bytes, `data[0]` is msgType and `data[1:]` is the payload. Callers of extension messages then read `payload[0]` as extID and `payload[1:]` as bencode.

**Piece data location in ut_metadata response.** The bencode dict is at the start of the extension payload (after extID). Raw piece bytes follow immediately after the bencode dict in the same message. Use `bytes.NewReader` + `bencode.NewDecoder` to decode the dict, then `io.ReadAll` the remaining bytes from the same reader.

**`defer crawler.Stop()` after a failed `crawler.Start()`.** Safe because `crawler.Stop()` nil-checks `c.cancel` before invoking it.

**`UpsertTorrentPending` uses `ON CONFLICT DO NOTHING`.** The bloom filter in the DHT layer prevents most duplicates, but concurrent goroutines in the ingest worker can race on the same infohash. The DB-level dedup check (`GetTorrentByInfohash`) reduces this further, but the upsert is the final safety net. `RowsAffected() == 0` means the conflict branch fired; skip file inserts.
