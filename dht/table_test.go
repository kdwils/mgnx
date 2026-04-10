package dht

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testCfg() config.DHT {
	return config.DHT{
		BucketSize:          8,
		BadFailureThreshold: 2,
		StaleThreshold:      15 * time.Minute,
	}
}

func makeNode(id byte, port int) *Node {
	var nodeID NodeID
	nodeID[0] = id
	return &Node{
		ID:       nodeID,
		Addr:     &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port},
		LastSeen: time.Now(),
	}
}

func TestRoutingTable_Insert(t *testing.T) {
	t.Run("inserts node into empty table", func(t *testing.T) {
		var ourID NodeID
		rt := NewRoutingTable(ourID, testCfg(), nil)
		n := makeNode(0x10, 1000)
		rt.Insert(n)
		assert.Equal(t, 1, len(rt.buckets[0].Nodes))
	})

	t.Run("updates existing node without duplicating", func(t *testing.T) {
		var ourID NodeID
		rt := NewRoutingTable(ourID, testCfg(), nil)
		n := makeNode(0x10, 1000)
		rt.Insert(n)
		rt.Insert(n)
		assert.Equal(t, 1, len(rt.buckets[0].Nodes))
	})

	t.Run("splits bucket when full and contains our ID", func(t *testing.T) {
		// ourID = 0x00..00. Insert 8 nodes with IDs alternating across the
		// low and high halves of the keyspace to fill the single initial bucket,
		// then insert a 9th to trigger the split.
		var ourID NodeID
		rt := NewRoutingTable(ourID, testCfg(), nil)

		// 4 low-half IDs (0x01, 0x03, 0x05, 0x07) + 4 high-half IDs (0x82, 0x84, 0x86, 0x88)
		lowIDs := []byte{0x01, 0x03, 0x05, 0x07}
		highIDs := []byte{0x82, 0x84, 0x86, 0x88}
		for i, id := range lowIDs {
			rt.Insert(makeNode(id, 1000+i))
		}
		for i, id := range highIDs {
			rt.Insert(makeNode(id, 2000+i))
		}

		// 9th insert overflows the bucket, triggering a split around ourID.
		rt.Insert(makeNode(0x09, 3000))

		assert.Greater(t, len(rt.buckets), 1)
	})

	t.Run("adds to replacement cache when bucket is full and does not contain our ID", func(t *testing.T) {
		// ourID = 0xFF..FF so the low half [0x00, 0x80) never contains ourID.
		var ourID NodeID
		for i := range ourID {
			ourID[i] = 0xFF
		}
		rt := NewRoutingTable(ourID, testCfg(), nil)

		// Fill the bucket covering the low range with 8 nodes (IDs 0x00..0x07).
		for i := range 8 {
			rt.Insert(makeNode(byte(i), 1000+i))
		}

		// The 9th low-range node should go to the replacement cache.
		extra := makeNode(0x08, 2000)
		rt.Insert(extra)

		var lowBucket *Bucket
		for _, b := range rt.buckets {
			if b.Contains(extra.ID) {
				lowBucket = b
				break
			}
		}
		require.NotNil(t, lowBucket)
		assert.Equal(t, 1, len(lowBucket.ReplacementCache))
	})
}

func TestRoutingTable_Closest(t *testing.T) {
	t.Run("returns nodes sorted by XOR distance", func(t *testing.T) {
		var ourID NodeID
		rt := NewRoutingTable(ourID, testCfg(), nil)

		var target NodeID
		target[0] = 0x10

		ids := []byte{0x10, 0x20, 0x30}
		for i, id := range ids {
			rt.Insert(makeNode(id, 1000+i))
		}

		got := rt.Closest(target, 3)
		require.Len(t, got, 3)
		assert.Equal(t, NodeID{0x10}, got[0].ID) // distance 0 — exact match
	})

	t.Run("returns at most n nodes", func(t *testing.T) {
		var ourID NodeID
		rt := NewRoutingTable(ourID, testCfg(), nil)
		for i := range 5 {
			rt.Insert(makeNode(byte(0x10+i), 1000+i))
		}
		got := rt.Closest(ourID, 3)
		assert.Equal(t, 3, len(got))
	})

	t.Run("excludes bad nodes", func(t *testing.T) {
		var ourID NodeID
		rt := NewRoutingTable(ourID, testCfg(), nil)
		n := makeNode(0x10, 1000)
		rt.Insert(n)
		rt.MarkFailure(n.ID)
		rt.MarkFailure(n.ID)

		got := rt.Closest(n.ID, 8)
		for _, node := range got {
			assert.False(t, node.IsBad(testCfg().BadFailureThreshold))
		}
	})

	t.Run("returns empty slice for empty table", func(t *testing.T) {
		var ourID NodeID
		rt := NewRoutingTable(ourID, testCfg(), nil)
		got := rt.Closest(ourID, 8)
		assert.Empty(t, got)
	})
}

func TestRoutingTable_MarkSuccess(t *testing.T) {
	t.Run("resets failure count and updates last seen", func(t *testing.T) {
		var ourID NodeID
		rt := NewRoutingTable(ourID, testCfg(), nil)
		n := makeNode(0x10, 1000)
		n.FailureCount = 1
		n.LastSeen = time.Now().Add(-1 * time.Hour)
		rt.Insert(n)

		before := time.Now()
		rt.MarkSuccess(n.ID)

		b := rt.buckets[rt.bucketFor(n.ID)]
		var found *Node
		for _, node := range b.Nodes {
			if node.ID == n.ID {
				found = node
				break
			}
		}
		require.NotNil(t, found)
		assert.Equal(t, 0, found.FailureCount)
		assert.True(t, !found.LastSeen.Before(before))
	})
}

func TestRoutingTable_MarkFailure(t *testing.T) {
	t.Run("increments failure count", func(t *testing.T) {
		var ourID NodeID
		rt := NewRoutingTable(ourID, testCfg(), nil)
		n := makeNode(0x10, 1000)
		rt.Insert(n)
		rt.MarkFailure(n.ID)

		b := rt.buckets[rt.bucketFor(n.ID)]
		var found *Node
		for _, node := range b.Nodes {
			if node.ID == n.ID {
				found = node
				break
			}
		}
		require.NotNil(t, found)
		assert.Equal(t, 1, found.FailureCount)
	})

	t.Run("evicts bad node and promotes replacement", func(t *testing.T) {
		var ourID NodeID
		for i := range ourID {
			ourID[i] = 0xFF
		}
		rt := NewRoutingTable(ourID, testCfg(), nil)

		// Fill low bucket.
		for i := range 8 {
			rt.Insert(makeNode(byte(i), 1000+i))
		}
		// Add a replacement.
		replacement := makeNode(0x08, 2000)
		rt.Insert(replacement)

		// Make the LRS node bad.
		lrs := makeNode(0x00, 1000)
		rt.MarkFailure(lrs.ID)
		rt.MarkFailure(lrs.ID)

		b := rt.buckets[rt.bucketFor(lrs.ID)]
		for _, node := range b.Nodes {
			assert.NotEqual(t, lrs.ID, node.ID, "bad node should have been evicted")
		}
		assert.Equal(t, 0, len(b.ReplacementCache))
	})
}

func TestRoutingTable_StaleBuckets(t *testing.T) {
	t.Run("returns empty when no buckets are stale", func(t *testing.T) {
		var ourID NodeID
		rt := NewRoutingTable(ourID, testCfg(), nil)
		assert.Empty(t, rt.StaleBuckets())
	})

	t.Run("returns stale buckets", func(t *testing.T) {
		var ourID NodeID
		rt := NewRoutingTable(ourID, testCfg(), nil)
		rt.buckets[0].LastChanged = time.Now().Add(-20 * time.Minute)
		assert.Equal(t, 1, len(rt.StaleBuckets()))
	})
}

func TestRoutingTable_SaveLoad(t *testing.T) {
	t.Run("round-trips good nodes through file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "nodes.dat")

		cfg := testCfg()
		cfg.NodesPath = path

		var ourID NodeID
		rt := NewRoutingTable(ourID, cfg, nil)

		n1 := makeNode(0x10, 1001)
		n2 := makeNode(0x20, 1002)
		rt.Insert(n1)
		rt.Insert(n2)

		require.NoError(t, rt.Save(t.Context()))

		rt2 := NewRoutingTable(ourID, cfg, nil)
		require.NoError(t, rt2.Load())

		got := rt2.Closest(ourID, 8)
		assert.Equal(t, 2, len(got))
	})

	t.Run("returns nil for missing file", func(t *testing.T) {
		cfg := testCfg()
		cfg.NodesPath = filepath.Join(t.TempDir(), "nonexistent.dat")

		var ourID NodeID
		rt := NewRoutingTable(ourID, cfg, nil)
		assert.NoError(t, rt.Load())
	})

	t.Run("returns error for corrupt file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "nodes.dat")
		require.NoError(t, os.WriteFile(path, []byte("bad"), 0o644))

		cfg := testCfg()
		cfg.NodesPath = path

		var ourID NodeID
		rt := NewRoutingTable(ourID, cfg, nil)
		assert.Error(t, rt.Load())
	})
}
