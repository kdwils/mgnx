package dht

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/logger"
)

// Pinger is used by the routing table to perform async ping-before-evict.
// Implementations must call MarkSuccess or MarkFailure on the table once the
// ping completes.
type Pinger interface {
	PingAsync(ctx context.Context, node *Node)
}

// RoutingTable manages the Kademlia k-bucket routing table as a sorted slice
// of buckets covering the full 160-bit keyspace.
type RoutingTable struct {
	mu      sync.RWMutex
	buckets []*Bucket
	ourID   NodeID
	cfg     config.DHT
	pinger  Pinger
}

// NewRoutingTable creates a routing table with a single bucket covering the
// full keyspace [0, 2^160). pinger may be nil; ping-before-evict is skipped
// until the server is wired up.
func NewRoutingTable(ourID NodeID, cfg config.DHT, pinger Pinger) *RoutingTable {
	return &RoutingTable{
		buckets: []*Bucket{{LastChanged: time.Now()}}, // Min=Max={} sentinel = full keyspace
		ourID:   ourID,
		cfg:     cfg,
		pinger:  pinger,
	}
}

// SetPinger wires the ping-before-evict implementation after the server is created.
func (rt *RoutingTable) SetPinger(p Pinger) {
	rt.mu.Lock()
	rt.pinger = p
	rt.mu.Unlock()
}

// SetOurID updates the table's own node ID. Called once during bootstrap when
// the BEP-42 compliant ID is derived.
func (rt *RoutingTable) SetOurID(id NodeID) {
	rt.mu.Lock()
	rt.ourID = id
	rt.mu.Unlock()
}

// Insert adds node to the routing table or updates it if already present.
func (rt *RoutingTable) Insert(node *Node) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.insert(node)
}

// insert is the lock-free inner implementation; callers must hold rt.mu.
func (rt *RoutingTable) insert(node *Node) {
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
			b.LastChanged = time.Now()
			return
		}

		if !b.IsFull(rt.cfg.BucketSize) {
			b.Nodes = append(b.Nodes, node)
			b.LastChanged = time.Now()
			return
		}

		if b.Contains(rt.ourID) {
			left, right := rt.splitBucket(idx)
			rt.buckets = bucketSliceReplace(rt.buckets, idx, left, right)
			continue
		}

		// Bucket is full and does not contain our ID: ping-before-evict.
		// Add the candidate to the replacement cache and async-ping the LRS node.
		if len(b.ReplacementCache) < rt.cfg.BucketSize {
			b.ReplacementCache = append(b.ReplacementCache, node)
		}
		if rt.pinger == nil || len(b.Nodes) == 0 {
			return
		}
		lrs := b.Nodes[0] // slice is ordered by LastSeen ascending
		go rt.pinger.PingAsync(context.Background(), lrs)
		return
	}
}

// Closest returns the n nodes closest to target by XOR distance, excluding
// bad nodes.
func (rt *RoutingTable) Closest(target NodeID, n int) []*Node {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	var candidates []*Node
	for _, b := range rt.buckets {
		for _, node := range b.Nodes {
			if node.IsBad(rt.cfg.BadFailureThreshold) {
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
		b.LastChanged = time.Now()
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
		if !n.IsBad(rt.cfg.BadFailureThreshold) || len(b.ReplacementCache) == 0 {
			return
		}
		b.Nodes = append(b.Nodes[:i], b.Nodes[i+1:]...)
		replacement := b.ReplacementCache[0]
		b.ReplacementCache = b.ReplacementCache[1:]
		b.Nodes = append(b.Nodes, replacement)
		b.LastChanged = time.Now()
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
		if b.IsStale(rt.cfg.StaleThreshold) {
			stale = append(stale, b)
		}
	}
	return stale
}

// Save writes all good nodes to cfg.NodesPath as consecutive 26-byte compact
// records using an atomic write (temp file + rename).
func (rt *RoutingTable) Save(ctx context.Context) error {
	logger := logger.FromContext(ctx)
	path := rt.cfg.NodesPath
	// TODO: make this cleaner, we should have a dedicated package for file io + dep injection
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmpPath := path + ".tmp"

	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	rt.mu.RLock()
	var goodNodes []*Node
	for _, b := range rt.buckets {
		for _, n := range b.Nodes {
			if !n.IsBad(rt.cfg.BadFailureThreshold) {
				goodNodes = append(goodNodes, n)
			}
		}
	}
	rt.mu.RUnlock()

	if _, err := f.WriteString(EncodeNodes(goodNodes)); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	logger.Debug("routing table saved", "service", "dht", "nodes", len(goodNodes), "path", path)
	return nil
}

// Load reads nodes from cfg.NodesPath and inserts them into the routing table.
// Returns nil when the file does not exist (first run).
func (rt *RoutingTable) Load() error {
	data, err := os.ReadFile(rt.cfg.NodesPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	nodes, err := DecodeNodes(string(data))
	if err != nil {
		return err
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, n := range nodes {
		n.LastSeen = time.Now()
		rt.insert(n)
	}
	return nil
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
