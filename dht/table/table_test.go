package table

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/dht/table/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
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
		var serverNodeID NodeID
		rt := NewRoutingTable(serverNodeID, testCfg(), nil)
		n := makeNode(0x10, 1000)
		rt.Insert(context.Background(), n)
		assert.Equal(t, 1, len(rt.buckets[0].Nodes))
	})

	t.Run("updates existing node without duplicating", func(t *testing.T) {
		var serverNodeID NodeID
		rt := NewRoutingTable(serverNodeID, testCfg(), nil)
		n := makeNode(0x10, 1000)
		rt.Insert(context.Background(), n)
		rt.Insert(context.Background(), n)
		assert.Equal(t, 1, len(rt.buckets[0].Nodes))
	})

	t.Run("splits bucket when full and contains our ID", func(t *testing.T) {
		// serverNodeID = 0x00..00. Insert 8 nodes with IDs alternating across the
		// low and high halves of the keyspace to fill the single initial bucket,
		// then insert a 9th to trigger the split.
		var serverNodeID NodeID
		rt := NewRoutingTable(serverNodeID, testCfg(), nil)

		// 4 low-half IDs (0x01, 0x03, 0x05, 0x07) + 4 high-half IDs (0x82, 0x84, 0x86, 0x88)
		lowIDs := []byte{0x01, 0x03, 0x05, 0x07}
		highIDs := []byte{0x82, 0x84, 0x86, 0x88}
		for i, id := range lowIDs {
			rt.Insert(context.Background(), makeNode(id, 1000+i))
		}
		for i, id := range highIDs {
			rt.Insert(context.Background(), makeNode(id, 2000+i))
		}

		// 9th insert overflows the bucket, triggering a split around serverNodeID.
		rt.Insert(context.Background(), makeNode(0x09, 3000))

		assert.Greater(t, len(rt.buckets), 1)
	})

	t.Run("adds to replacement cache when bucket is full and does not contain our ID", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		pinger := mocks.NewMockPinger(ctrl)

		// serverNodeID = 0xFF..FF so the low half [0x00, 0x80) never contains serverNodeID.
		var serverNodeID NodeID
		for i := range serverNodeID {
			serverNodeID[i] = 0xFF
		}
		rt := NewRoutingTable(serverNodeID, testCfg(), pinger)

		for i := range 8 {
			rt.Insert(context.Background(), makeNode(byte(i), 1000+i))
		}

		extra := makeNode(0x08, 2000)

		pingCalled := make(chan struct{})
		pinger.EXPECT().Ping(gomock.Any(), gomock.Any(), NodeID{0x00}).Do(func(ctx, addr, id any) {
			close(pingCalled)
		}).Return(nil, nil)

		rt.Insert(context.Background(), extra)

		select {
		case <-pingCalled:
		case <-time.After(time.Second):
			t.Fatal("ping was not called")
		}

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

	t.Run("deduplicates repeated inserts into replacement cache", func(t *testing.T) {
		var serverNodeID NodeID
		for i := range serverNodeID {
			serverNodeID[i] = 0xFF
		}
		rt := NewRoutingTable(serverNodeID, testCfg(), nil)

		for i := range 8 {
			rt.Insert(context.Background(), makeNode(byte(i), 1000+i))
		}

		// Insert the same overflow node twice with a different address the second
		// time. The cache should contain exactly one entry with the updated addr.
		n := makeNode(0x08, 2000)
		rt.Insert(context.Background(), n)

		updated := makeNode(0x08, 3000)
		rt.Insert(context.Background(), updated)

		var lowBucket *Bucket
		for _, b := range rt.buckets {
			if b.Contains(n.ID) {
				lowBucket = b
				break
			}
		}
		require.NotNil(t, lowBucket)
		assert.Equal(t, 1, len(lowBucket.ReplacementCache), "duplicate insert must not grow the cache")
		assert.Equal(t, updated.Addr, lowBucket.ReplacementCache[0].Addr, "addr should be updated in-place")
	})
}

func TestRoutingTable_Closest(t *testing.T) {
	t.Run("returns nodes sorted by XOR distance", func(t *testing.T) {
		var serverNodeID NodeID
		rt := NewRoutingTable(serverNodeID, testCfg(), nil)

		var target NodeID
		target[0] = 0x10

		ids := []byte{0x10, 0x20, 0x30}
		for i, id := range ids {
			rt.Insert(context.Background(), makeNode(id, 1000+i))
		}

		got := rt.Closest(target, 3)
		require.Len(t, got, 3)
		assert.Equal(t, NodeID{0x10}, got[0].ID) // distance 0 — exact match
	})

	t.Run("returns at most n nodes", func(t *testing.T) {
		var serverNodeID NodeID
		rt := NewRoutingTable(serverNodeID, testCfg(), nil)
		for i := range 5 {
			rt.Insert(context.Background(), makeNode(byte(0x10+i), 1000+i))
		}
		got := rt.Closest(serverNodeID, 3)
		assert.Equal(t, 3, len(got))
	})

	t.Run("excludes bad nodes", func(t *testing.T) {
		var serverNodeID NodeID
		rt := NewRoutingTable(serverNodeID, testCfg(), nil)
		n := makeNode(0x10, 1000)
		rt.Insert(context.Background(), n)
		rt.MarkFailure(n.ID)
		rt.MarkFailure(n.ID)

		got := rt.Closest(n.ID, 8)
		for _, node := range got {
			assert.False(t, node.IsBad(testCfg().BadFailureThreshold))
		}
	})

	t.Run("returns empty slice for empty table", func(t *testing.T) {
		var serverNodeID NodeID
		rt := NewRoutingTable(serverNodeID, testCfg(), nil)
		got := rt.Closest(serverNodeID, 8)
		assert.Empty(t, got)
	})

	t.Run("includes replacement cache nodes as candidates", func(t *testing.T) {
		// serverNodeID = 0xFF..FF so the low-keyspace bucket never contains serverNodeID and
		// will never split — overflow nodes go to ReplacementCache.
		var serverNodeID NodeID
		for i := range serverNodeID {
			serverNodeID[i] = 0xFF
		}
		rt := NewRoutingTable(serverNodeID, testCfg(), nil)

		// Fill the bucket (BucketSize=8) with nodes that are far from target.
		for i := range 8 {
			rt.Insert(context.Background(), makeNode(byte(0x50+i), 1000+i))
		}

		// This node is closer to target but goes to the replacement cache because
		// the bucket is full.
		cacheNode := makeNode(0x01, 2000)
		rt.Insert(context.Background(), cacheNode)

		var target NodeID
		target[0] = 0x01

		// Without the fix, cacheNode would never appear. With the fix it should
		// be the closest candidate returned.
		got := rt.Closest(target, 9)
		var found bool
		for _, n := range got {
			if n.ID == cacheNode.ID {
				found = true
				break
			}
		}
		assert.True(t, found, "replacement cache node should be visible to Closest()")
	})

	t.Run("excludes bad replacement cache nodes", func(t *testing.T) {
		var serverNodeID NodeID
		for i := range serverNodeID {
			serverNodeID[i] = 0xFF
		}
		rt := NewRoutingTable(serverNodeID, testCfg(), nil)

		for i := range 8 {
			rt.Insert(context.Background(), makeNode(byte(0x50+i), 1000+i))
		}

		badNode := makeNode(0x01, 2000)
		rt.Insert(context.Background(), badNode)

		// Drive the cache node to bad status via MarkFailure.
		rt.MarkFailure(badNode.ID)
		rt.MarkFailure(badNode.ID)

		var target NodeID
		target[0] = 0x01

		got := rt.Closest(target, 9)
		for _, n := range got {
			assert.NotEqual(t, badNode.ID, n.ID, "bad replacement cache node must be excluded")
		}
	})
}

func TestRoutingTable_MarkSuccess(t *testing.T) {
	t.Run("resets failure count and updates last seen", func(t *testing.T) {
		var serverNodeID NodeID
		rt := NewRoutingTable(serverNodeID, testCfg(), nil)
		n := makeNode(0x10, 1000)
		n.FailureCount = 1
		n.LastSeen = time.Now().Add(-1 * time.Hour)
		rt.Insert(context.Background(), n)

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
		var serverNodeID NodeID
		rt := NewRoutingTable(serverNodeID, testCfg(), nil)
		n := makeNode(0x10, 1000)
		rt.Insert(context.Background(), n)
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
		var serverNodeID NodeID
		for i := range serverNodeID {
			serverNodeID[i] = 0xFF
		}
		rt := NewRoutingTable(serverNodeID, testCfg(), nil)

		// Fill low bucket.
		for i := range 8 {
			rt.Insert(context.Background(), makeNode(byte(i), 1000+i))
		}
		// Add a replacement.
		replacement := makeNode(0x08, 2000)
		rt.Insert(context.Background(), replacement)

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

	t.Run("prunes bad nodes from replacement cache", func(t *testing.T) {
		var serverNodeID NodeID
		for i := range serverNodeID {
			serverNodeID[i] = 0xFF
		}
		rt := NewRoutingTable(serverNodeID, testCfg(), nil)

		for i := range 8 {
			rt.Insert(context.Background(), makeNode(byte(i), 1000+i))
		}

		// Two nodes in the cache: one will go bad, the other must survive.
		bad := makeNode(0x08, 2000)
		good := makeNode(0x09, 2001)
		rt.Insert(context.Background(), bad)
		rt.Insert(context.Background(), good)

		b := rt.buckets[rt.bucketFor(bad.ID)]
		require.Equal(t, 2, len(b.ReplacementCache))

		// Drive bad node to BadFailureThreshold (2 failures).
		rt.MarkFailure(bad.ID)
		rt.MarkFailure(bad.ID)

		assert.Equal(t, 1, len(b.ReplacementCache), "bad cache node should be pruned")
		assert.Equal(t, good.ID, b.ReplacementCache[0].ID, "good cache node should remain")
	})
}

func TestRoutingTable_StaleBuckets(t *testing.T) {
	t.Run("returns empty when no buckets are stale", func(t *testing.T) {
		var serverNodeID NodeID
		rt := NewRoutingTable(serverNodeID, testCfg(), nil)
		assert.Empty(t, rt.StaleBuckets())
	})

	t.Run("returns stale buckets", func(t *testing.T) {
		var serverNodeID NodeID
		rt := NewRoutingTable(serverNodeID, testCfg(), nil)
		rt.buckets[0].LastChanged = time.Now().Add(-20 * time.Minute)
		assert.Equal(t, 1, len(rt.StaleBuckets()))
	})
}
