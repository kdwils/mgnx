package table

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/dht/krpc"
)

// Pinger is used by the routing table to perform async ping-before-evict.
// Implementations must call MarkSuccess or MarkFailure on the table once the
// ping completes.
//
//go:generate go run go.uber.org/mock/mockgen -destination=mocks/mock_pinger.go -package=mocks github.com/kdwils/mgnx/dht/table Pinger
type Pinger interface {
	Ping(ctx context.Context, addr *net.UDPAddr, id [20]byte) (*krpc.Msg, error)
}

// RoutingTable manages the Kademlia k-bucket routing table as a sorted slice
// of buckets covering the full 160-bit keyspace.
type RoutingTable struct {
	mu                  sync.RWMutex
	buckets             []*Bucket
	nodeID              NodeID
	bucketSize          int
	badFailureThreshold int
	staleThreshold      time.Duration
	pinger              Pinger
}

// NewRoutingTable creates a routing table with a single bucket covering the
// full keyspace [0, 2^160). pinger may be nil; ping-before-evict is skipped
// until the server is wired up.
func NewRoutingTable(nodeID NodeID, cfg config.DHT, pinger Pinger) *RoutingTable {
	return &RoutingTable{
		buckets:             []*Bucket{{LastChanged: time.Now()}}, // Min=Max={} sentinel = full keyspace
		nodeID:              nodeID,
		bucketSize:          cfg.BucketSize,
		badFailureThreshold: cfg.BadFailureThreshold,
		staleThreshold:      cfg.StaleThreshold,
		pinger:              pinger,
	}
}

// NodeInsertResult describes the outcome of a routing table Insert call.
const (
	NodeInsertUpdated  = "updated"  // node already present; address/liveness refreshed
	NodeInsertInserted = "inserted" // new node added to an active bucket
	NodeInsertCached   = "cached"   // bucket full; node added to replacement cache
	NodeInsertDropped  = "dropped"  // bucket and replacement cache both full; node discarded
)

// Insert adds node to the routing table or updates it if already present.
// It returns one of the NodeInsert* constants describing the outcome.
func (rt *RoutingTable) Insert(ctx context.Context, node *Node) string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.insert(ctx, node)
}

// insert is the lock-free inner implementation; callers must hold rt.mu.
func (rt *RoutingTable) insert(ctx context.Context, node *Node) string {
	for {
		idx := rt.bucketFor(node.ID)
		b := rt.buckets[idx]

		for _, n := range b.Nodes {
			if n.ID != node.ID {
				continue
			}
			n.Addr = node.Addr
			n.LastSeen = node.LastSeen
			n.FailureCount = 0
			return NodeInsertUpdated
		}

		if !b.IsFull(rt.bucketSize) {
			b.Nodes = append(b.Nodes, node)
			b.LastChanged = time.Now()
			return NodeInsertInserted
		}

		if b.Contains(rt.nodeID) {
			left, right := rt.splitBucket(idx)
			rt.buckets = bucketSliceReplace(rt.buckets, idx, left, right)
			continue
		}

		// Bucket is full and does not contain our ID: ping-before-evict.
		// Add the candidate to the replacement cache and async-ping the LRS node.
		for _, n := range b.ReplacementCache {
			if n.ID != node.ID {
				continue
			}
			n.Addr = node.Addr
			n.LastSeen = node.LastSeen
			n.FailureCount = 0
			return NodeInsertUpdated
		}
		if len(b.ReplacementCache) < rt.bucketSize {
			b.ReplacementCache = append(b.ReplacementCache, node)
			if rt.pinger != nil && len(b.Nodes) != 0 {
				lrs := b.Nodes[0]
				go rt.pinger.Ping(ctx, lrs.Addr, lrs.ID)
			}
			return NodeInsertCached
		}
		return NodeInsertDropped
	}
}

// Closest returns the n nodes closest to target by XOR distance, excluding
// bad nodes. Both active nodes and replacement cache nodes are considered as
// candidates so that recently discovered nodes not yet promoted to active
// buckets are reachable by the crawler.
func (rt *RoutingTable) Closest(target NodeID, n int) []*Node {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	var candidates []*Node
	for _, b := range rt.buckets {
		for _, node := range b.Nodes {
			if node.IsBad(rt.badFailureThreshold) {
				continue
			}
			candidates = append(candidates, node)
		}
		for _, node := range b.ReplacementCache {
			if node.IsBad(rt.badFailureThreshold) {
				continue
			}
			candidates = append(candidates, node)
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		di := target.XOR(candidates[i].ID)
		dj := target.XOR(candidates[j].ID)
		return bytes.Compare(di[:], dj[:]) < 0
	})

	if n > len(candidates) {
		n = len(candidates)
	}
	return candidates[:n]
}

// MarkSuccess updates a node's LastSeen and resets its FailureCount.
// LastChanged is not updated here — it tracks membership changes (adds/evictions)
// only, so that StaleBuckets correctly identifies buckets that need a find_node
// refresh to discover new nodes.
func (rt *RoutingTable) MarkSuccess(id NodeID) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	b := rt.buckets[rt.bucketFor(id)]
	for _, n := range b.Nodes {
		if n.ID != id {
			continue
		}
		n.LastSeen = time.Now()
		n.FailureCount = 0
		return
	}
}

// MarkFailure increments a node's FailureCount. If the node becomes bad and
// the replacement cache is non-empty, the bad node is evicted and the first
// replacement is promoted.
func (rt *RoutingTable) MarkFailure(id NodeID) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	b := rt.buckets[rt.bucketFor(id)]
	for i, n := range b.Nodes {
		if n.ID != id {
			continue
		}
		n.FailureCount++
		if !n.IsBad(rt.badFailureThreshold) || len(b.ReplacementCache) == 0 {
			return
		}
		b.Nodes = append(b.Nodes[:i], b.Nodes[i+1:]...)
		replacement := b.ReplacementCache[0]
		b.ReplacementCache = b.ReplacementCache[1:]
		b.Nodes = append(b.Nodes, replacement)
		b.LastChanged = time.Now()
		return
	}

	// Prune failing nodes from the replacement cache so dead nodes do not
	// accumulate once Closest() starts returning them as candidates.
	for i := len(b.ReplacementCache) - 1; i >= 0; i-- {
		n := b.ReplacementCache[i]
		if n.ID != id {
			continue
		}
		n.FailureCount++
		if n.IsBad(rt.badFailureThreshold) {
			b.ReplacementCache = append(b.ReplacementCache[:i], b.ReplacementCache[i+1:]...)
		}
		return
	}
}

// NodeCount returns the total number of nodes across all buckets.
func (rt *RoutingTable) NodeCount() int {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	n := 0
	for _, b := range rt.buckets {
		n += len(b.Nodes)
	}
	return n
}

// StaleBuckets returns all buckets whose LastChanged exceeds the stale
// threshold. The caller uses these to trigger refresh find_node queries.
func (rt *RoutingTable) StaleBuckets() []*Bucket {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	var stale []*Bucket
	for _, b := range rt.buckets {
		if b.IsStale(rt.staleThreshold) {
			stale = append(stale, b)
		}
	}
	return stale
}

// bucketFor returns the index of the bucket containing id.
// Must be called with rt.mu held.
func (rt *RoutingTable) bucketFor(id NodeID) int {
	idx := sort.Search(len(rt.buckets), func(i int) bool {
		return bytes.Compare(rt.buckets[i].Min[:], id[:]) > 0
	})
	if idx > 0 {
		idx--
	}
	return idx
}

// splitBucket splits the bucket at idx into two equal halves and returns them
// with nodes redistributed. The split point is b.Min with bit Depth set to 1
// and all lower bits cleared. This is correct for dyadic (power-of-2) intervals
// where Depth encodes the number of fixed prefix bits.
// Must be called with rt.mu held.
func (rt *RoutingTable) splitBucket(idx int) (*Bucket, *Bucket) {
	b := rt.buckets[idx]
	d := b.Depth

	// Bit 0 of a NodeID is the MSB of byte 0 (big-endian 160-bit integer).
	byteIdx := d / 8
	bitIdx := 7 - (d % 8)

	var mid NodeID
	copy(mid[:], b.Min[:])
	mid[byteIdx] |= 1 << bitIdx
	mid[byteIdx] &^= (1 << bitIdx) - 1 // clear bits below the split bit
	for j := byteIdx + 1; j < 20; j++ {
		mid[j] = 0
	}

	now := time.Now()
	left := &Bucket{Min: b.Min, Max: mid, Depth: d + 1, LastChanged: now}
	right := &Bucket{Min: mid, Max: b.Max, Depth: d + 1, LastChanged: now}

	for _, n := range b.Nodes {
		if left.Contains(n.ID) {
			left.Nodes = append(left.Nodes, n)
			continue
		}
		right.Nodes = append(right.Nodes, n)
	}
	for _, n := range b.ReplacementCache {
		if left.Contains(n.ID) {
			left.ReplacementCache = append(left.ReplacementCache, n)
			continue
		}
		right.ReplacementCache = append(right.ReplacementCache, n)
	}
	return left, right
}

// bucketSliceReplace replaces the element at idx with left and right,
// returning the new slice.
func bucketSliceReplace(s []*Bucket, idx int, left, right *Bucket) []*Bucket {
	result := make([]*Bucket, len(s)+1)
	copy(result, s[:idx])
	result[idx] = left
	result[idx+1] = right
	copy(result[idx+2:], s[idx+1:])
	return result
}

// EncodePeer encodes a single peer as a 6-byte compact peer record per BEP-05.
func EncodePeer(ip net.IP, port int) string {
	var buf [6]byte
	copy(buf[:4], ip.To4())
	binary.BigEndian.PutUint16(buf[4:6], uint16(port))
	return string(buf[:])
}

// DecodePeers parses a list of 6-byte compact peer strings into net.Addr values.
func DecodePeers(values []string) ([]net.Addr, error) {
	addrs := make([]net.Addr, 0, len(values))
	for _, v := range values {
		if len(v) != 6 {
			return nil, fmt.Errorf("krpc: peer record must be 6 bytes, got %d", len(v))
		}
		ip := make(net.IP, 4)
		copy(ip, v[:4])
		port := int(binary.BigEndian.Uint16([]byte(v[4:6])))
		addrs = append(addrs, &net.TCPAddr{IP: ip, Port: port})
	}
	return addrs, nil
}

// EncodeNodes encodes a slice of nodes as a concatenated sequence of 26-byte
// compact node records per BEP-05 §Compact Node Info.
// Non-IPv4 nodes are silently skipped.
// Format per node: [20]byte ID | [4]byte IPv4 big-endian | [2]byte port big-endian.
func EncodeNodes(nodes []*Node) string {
	buf := make([]byte, 0, len(nodes)*26)
	for _, n := range nodes {
		ip := n.Addr.IP.To4()
		if ip == nil {
			continue
		}
		var record [26]byte
		copy(record[:20], n.ID[:])
		copy(record[20:24], ip)
		binary.BigEndian.PutUint16(record[24:26], uint16(n.Addr.Port))
		buf = append(buf, record[:]...)
	}
	return string(buf)
}

// DecodeNodes parses a compact node string into a slice of nodes per
// BEP-05 §Compact Node Info.
// Returns an error if the length is not a multiple of 26.
func DecodeNodes(s string) ([]*Node, error) {
	if len(s)%26 != 0 {
		return nil, fmt.Errorf("dht: nodes length %d is not a multiple of 26", len(s))
	}
	nodes := make([]*Node, len(s)/26)
	for i := range nodes {
		off := i * 26
		var id NodeID
		copy(id[:], s[off:off+20])
		ip := make(net.IP, 4)
		copy(ip, s[off+20:off+24])
		port := int(binary.BigEndian.Uint16([]byte(s[off+24 : off+26])))
		nodes[i] = &Node{
			ID:   id,
			Addr: &net.UDPAddr{IP: ip, Port: port},
		}
	}
	return nodes, nil
}
