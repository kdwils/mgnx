package dht

import (
	"net"
	"testing"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBucketRefresh_sameNodeInMultipleClosestLists proves the mechanism behind
// the repeated-query bug observed in production (265 queries to 34.133.252.96
// in a single minute, with microsecond-spaced timestamps showing simultaneous
// hits from a single refresh tick).
//
// When multiple stale buckets exist, bucketRefreshLoop calls
// Closest(target, k) once per bucket. Because Closest searches the entire
// routing table by XOR distance, the same node can appear in the top-k for
// several different bucket targets — especially when the routing table is
// sparse relative to k. Without deduplication across the tick, that node
// receives one query per appearance.
//
// Concretely: with BucketSize=2 and three nodes split across two buckets,
// nodeA (0x10…) sits in the left bucket alongside nodeC (0x20…). nodeB
// (0x80…) is in the right bucket alone. For a right-half target (0xC0…):
//
//	XOR(nodeB, 0xC0) = 0x40  ← closest
//	XOR(nodeA, 0xC0) = 0xD0  ← second closest
//	XOR(nodeC, 0xC0) = 0xE0
//
// So Closest(right-target, 2) = [nodeB, nodeA].
// Closest(left-target, 2)     = [nodeA, nodeC].
//
// nodeA appears in both — without deduplication it is queried twice per tick.
func TestBucketRefresh_sameNodeInMultipleClosestLists(t *testing.T) {
	cfg := config.DHT{
		BucketSize:          2,
		StaleThreshold:      0, // all buckets immediately stale
		BadFailureThreshold: 2,
	}
	var ourID NodeID // all-zero → lives in the left (low) half after splits
	rt := NewRoutingTable(ourID, cfg, nil)

	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}

	// nodeA and nodeC have first byte < 0x80 (left half).
	// nodeB has first byte >= 0x80 (right half).
	var nodeAID, nodeBID, nodeCID NodeID
	nodeAID[0] = 0x10
	nodeBID[0] = 0x80
	nodeCID[0] = 0x20

	// Inserting nodeA and nodeC fills the single bucket (BucketSize=2).
	// Inserting nodeB triggers a split because the bucket is full and contains ourID.
	// After the split: left=[nodeA, nodeC], right=[nodeB].
	rt.Insert(&Node{ID: nodeAID, Addr: addr, LastSeen: time.Now()})
	rt.Insert(&Node{ID: nodeCID, Addr: addr, LastSeen: time.Now()})
	rt.Insert(&Node{ID: nodeBID, Addr: addr, LastSeen: time.Now()})

	staleBuckets := rt.StaleBuckets()
	require.Len(t, staleBuckets, 2, "expected two stale buckets after split")

	// Use deterministic targets — one in each bucket's range — to make the
	// XOR ordering predictable without relying on randomIDInBucket.
	var leftTarget, rightTarget NodeID
	leftTarget[0] = 0x40  // mid-point of [0x00, 0x80)
	rightTarget[0] = 0xC0 // mid-point of [0x80, 0xFF]

	targetFor := func(b *Bucket) NodeID {
		if b.Contains(leftTarget) {
			return leftTarget
		}
		return rightTarget
	}

	// ── Simulate the current (unfixed) refresh tick ───────────────────────
	// No deduplication: count how many times each node appears across all
	// bucket Closest lists.
	appearsCount := make(map[NodeID]int)
	for _, b := range staleBuckets {
		for _, n := range rt.Closest(targetFor(b), cfg.BucketSize) {
			appearsCount[n.ID]++
		}
	}

	// nodeA must appear in both lists — this is the bug.
	assert.Greater(t, appearsCount[nodeAID], 1,
		"nodeA should appear in multiple Closest lists, proving deduplication is needed")

	// ── Simulate the fixed refresh tick ──────────────────────────────────
	// With a per-tick seen set, each node is queried at most once.
	queried := make(map[NodeID]bool)
	for _, b := range staleBuckets {
		for _, n := range rt.Closest(targetFor(b), cfg.BucketSize) {
			if queried[n.ID] {
				continue
			}
			queried[n.ID] = true
		}
	}

	// All three nodes reached exactly once.
	assert.True(t, queried[nodeAID], "nodeA should be queried")
	assert.True(t, queried[nodeBID], "nodeB should be queried")
	assert.True(t, queried[nodeCID], "nodeC should be queried")
	assert.Len(t, queried, 3, "exactly 3 unique nodes queried after deduplication")
}

// TestBucketRefresh_markSuccessDoesNotResetStaleness verifies that a node
// responding to a query does NOT refresh its bucket's LastChanged timestamp.
// If it did, buckets containing active nodes would never become stale and
// bucketRefreshLoop would never fire — which was the root cause of the routing
// table stagnating at ~150 nodes in production.
func TestBucketRefresh_markSuccessDoesNotResetStaleness(t *testing.T) {
	cfg := config.DHT{
		BucketSize:          8,
		StaleThreshold:      50 * time.Millisecond,
		BadFailureThreshold: 2,
	}
	var ourID NodeID
	rt := NewRoutingTable(ourID, cfg, nil)

	var nodeID NodeID
	nodeID[0] = 0x10
	rt.Insert(&Node{ID: nodeID, Addr: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}, LastSeen: time.Now()})

	// Bucket was just populated — not stale yet.
	require.Empty(t, rt.StaleBuckets(), "bucket should not be stale immediately after insert")

	// Simulate the node responding to queries repeatedly.
	for i := 0; i < 5; i++ {
		rt.MarkSuccess(nodeID)
	}

	// Wait past the stale threshold.
	time.Sleep(75 * time.Millisecond)

	// Despite the node being active, the bucket must now be considered stale
	// so that bucketRefreshLoop fires find_node queries to discover new nodes.
	stale := rt.StaleBuckets()
	assert.Len(t, stale, 1, "bucket should be stale after threshold even with active node")
}
