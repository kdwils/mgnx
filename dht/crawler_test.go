package dht

import (
	"container/heap"
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeCrawler(t *testing.T) *crawler {
	t.Helper()
	c, err := NewCrawler(testServerCfg(t))
	require.NoError(t, err)
	t.Cleanup(c.Stop)
	return c.(*crawler)
}

func makeHarvestNode(id byte, port int) *Node {
	var nodeID NodeID
	nodeID[0] = id
	return &Node{
		ID:       nodeID,
		Addr:     &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port},
		LastSeen: time.Now(),
	}
}

func TestNewCrawler(t *testing.T) {
	t.Run("dedup is wired into server", func(t *testing.T) {
		c := makeCrawler(t)
		assert.NotNil(t, c.dedup)
		assert.Same(t, c.dedup, c.server.dedup)
	})

	t.Run("harvest channel is shared with server", func(t *testing.T) {
		c := makeCrawler(t)
		assert.Equal(t, c.harvest, c.server.harvest)
	})
}

func TestBep51PQ_ordering(t *testing.T) {
	t.Run("pop returns closest item first", func(t *testing.T) {
		var target NodeID

		var nearID NodeID
		nearID[0] = 0x01
		near := &bep51Item{node: makeHarvestNode(0x01, 1001), target: target, dist: target.XOR(nearID)}

		var farID NodeID
		farID[0] = 0xff
		far := &bep51Item{node: makeHarvestNode(0xff, 1002), target: target, dist: target.XOR(farID)}

		pq := &bep51PQ{}
		heap.Init(pq)
		heap.Push(pq, far)
		heap.Push(pq, near)

		first := heap.Pop(pq).(*bep51Item)
		assert.Equal(t, near.dist, first.dist)
	})

	t.Run("maintains heap invariant across multiple pushes", func(t *testing.T) {
		var target NodeID
		pq := &bep51PQ{}
		heap.Init(pq)

		for _, b := range []byte{0x50, 0x10, 0xff, 0x01, 0x80} {
			var id NodeID
			id[0] = b
			heap.Push(pq, &bep51Item{
				node:   makeHarvestNode(b, int(b)+1000),
				target: target,
				dist:   target.XOR(id),
			})
		}

		prev := heap.Pop(pq).(*bep51Item)
		for pq.Len() > 0 {
			next := heap.Pop(pq).(*bep51Item)
			assert.LessOrEqual(t, prev.dist[0], next.dist[0])
			prev = next
		}
	})
}

func TestCrawler_nextEligible(t *testing.T) {
	t.Run("returns nil for empty queue", func(t *testing.T) {
		c := makeCrawler(t)
		pq := &bep51PQ{}
		heap.Init(pq)
		assert.Nil(t, c.nextEligible(pq, make(map[NodeID]time.Time)))
	})

	t.Run("skips nodes within cooldown", func(t *testing.T) {
		c := makeCrawler(t)
		var target NodeID
		node := makeHarvestNode(0x01, 1001)
		pq := &bep51PQ{}
		heap.Init(pq)
		heap.Push(pq, &bep51Item{node: node, target: target, dist: target.XOR(node.ID)})

		seen := map[NodeID]time.Time{node.ID: time.Now().Add(time.Hour)}
		assert.Nil(t, c.nextEligible(pq, seen))
	})

	t.Run("returns node whose cooldown has expired", func(t *testing.T) {
		c := makeCrawler(t)
		var target NodeID
		node := makeHarvestNode(0x01, 1001)
		pq := &bep51PQ{}
		heap.Init(pq)
		heap.Push(pq, &bep51Item{node: node, target: target, dist: target.XOR(node.ID)})

		seen := map[NodeID]time.Time{node.ID: time.Now().Add(-time.Second)}
		item := c.nextEligible(pq, seen)
		require.NotNil(t, item)
		assert.Equal(t, node.ID, item.node.ID)
	})

	t.Run("returns unseen node immediately", func(t *testing.T) {
		c := makeCrawler(t)
		var target NodeID
		node := makeHarvestNode(0x02, 1002)
		pq := &bep51PQ{}
		heap.Init(pq)
		heap.Push(pq, &bep51Item{node: node, target: target, dist: target.XOR(node.ID)})

		item := c.nextEligible(pq, make(map[NodeID]time.Time))
		require.NotNil(t, item)
		assert.Equal(t, node.ID, item.node.ID)
	})
}

func TestCrawler_processSamples(t *testing.T) {
	t.Run("forwards new infohashes to harvest channel", func(t *testing.T) {
		c := makeCrawler(t)
		node := makeHarvestNode(0x01, 1001)
		item := &bep51Item{node: node}

		var h [20]byte
		h[0] = 0xAB
		c.processSamples(context.Background(), string(h[:]), item)

		select {
		case event := <-c.harvest:
			assert.Equal(t, h, event.Infohash)
			assert.Equal(t, node.Addr.Port, event.Port)
		case <-time.After(time.Second):
			t.Fatal("expected harvest event")
		}
	})

	t.Run("deduplicates repeated infohashes", func(t *testing.T) {
		c := makeCrawler(t)
		node := makeHarvestNode(0x01, 1001)
		item := &bep51Item{node: node}

		var h [20]byte
		h[0] = 0xCD
		c.processSamples(context.Background(), string(h[:]), item)
		c.processSamples(context.Background(), string(h[:]), item)

		<-c.harvest
		select {
		case <-c.harvest:
			t.Fatal("duplicate should be filtered by dedup")
		case <-time.After(50 * time.Millisecond):
		}
	})

	t.Run("ignores partial trailing bytes", func(t *testing.T) {
		c := makeCrawler(t)
		node := makeHarvestNode(0x01, 1001)
		item := &bep51Item{node: node}

		c.processSamples(context.Background(), string(make([]byte, 19)), item)

		select {
		case <-c.harvest:
			t.Fatal("partial bytes should not produce a harvest event")
		case <-time.After(50 * time.Millisecond):
		}
	})

	t.Run("drops silently when harvest channel is full", func(t *testing.T) {
		c := makeCrawler(t)
		node := makeHarvestNode(0x01, 1001)
		item := &bep51Item{node: node}

		for i := range cap(c.harvest) {
			c.harvest <- HarvestEvent{Infohash: [20]byte{byte(i)}}
		}

		var h [20]byte
		h[0] = 0xFF
		h[1] = 0xFF
		c.processSamples(context.Background(), string(h[:]), item) // must not block
	})

	t.Run("decodes multiple hashes from one samples string", func(t *testing.T) {
		c := makeCrawler(t)
		node := makeHarvestNode(0x01, 1001)
		item := &bep51Item{node: node}

		var buf [60]byte
		buf[0] = 0x01
		buf[20] = 0x02
		buf[40] = 0x03
		c.processSamples(context.Background(), string(buf[:]), item)

		count := 0
		timeout := time.After(time.Second)
		for count < 3 {
			select {
			case <-c.harvest:
				count++
			case <-timeout:
				t.Fatalf("expected 3 events, got %d", count)
			}
		}
	})
}

func TestCrawler_processNodes(t *testing.T) {
	t.Run("inserts decoded nodes into routing table and queue", func(t *testing.T) {
		c := makeCrawler(t)
		var target NodeID

		node := makeHarvestNode(0x10, 2000)
		encoded := EncodeNodes([]*Node{node})

		pq := &bep51PQ{}
		heap.Init(pq)
		c.processNodes(encoded, target, pq)

		assert.Equal(t, 1, pq.Len())
		item := heap.Pop(pq).(*bep51Item)
		assert.Equal(t, node.ID, item.node.ID)
		assert.Equal(t, target.XOR(node.ID), item.dist)
	})

	t.Run("ignores invalid encoded input", func(t *testing.T) {
		c := makeCrawler(t)
		var target NodeID

		pq := &bep51PQ{}
		heap.Init(pq)
		c.processNodes("bad", target, pq)
		assert.Equal(t, 0, pq.Len())
	})
}

func TestCrawler_seedQueue(t *testing.T) {
	t.Run("pushes closest routing table nodes onto queue", func(t *testing.T) {
		c := makeCrawler(t)

		for i := range 5 {
			c.server.table.Insert(makeHarvestNode(byte(i+1), 3000+i))
		}

		var target NodeID
		pq := &bep51PQ{}
		heap.Init(pq)
		c.seedQueue(pq, target)

		assert.Greater(t, pq.Len(), 0)
		for pq.Len() > 0 {
			item := heap.Pop(pq).(*bep51Item)
			assert.Equal(t, target, item.target)
			assert.Equal(t, target.XOR(item.node.ID), item.dist)
		}
	})
}
