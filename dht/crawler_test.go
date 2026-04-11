package dht

import (
	"container/heap"
	"context"
	"net"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/recorder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeCrawler(t *testing.T) *crawler {
	t.Helper()
	cr, err := NewCrawler(config.Crawler{
		Crawlers:             2,
		Alpha:                3,
		MaxIterations:        4,
		TraversalWidth:       20,
		DefaultCooldown:      2 * time.Second,
		DefaultInterval:      10 * time.Second,
		MaxNodeFailures:      3,
		MaxJitter:            500 * time.Millisecond,
		EmptySpinWait:        5 * time.Second,
		SampleEnqueueTimeout: 1 * time.Second,
		NodeCacheCleanup:     1 * time.Hour,
	}, testServerCfg(t), recorder.NewNoOp())
	require.NoError(t, err)
	cr.discoveryQueue = make(chan discoveryWork, 64)
	t.Cleanup(func() { cr.Stop(t.Context()) })
	return cr
}

func (c *crawler) wrapDiscoveryWorker(id int) *discoveryWorker {
	return &discoveryWorker{id: id, crawler: c}
}

// startDiscoveryWorkers launches discovery workers for tests that need the full
// processSamples → discoverPeers → discovered pipeline.
func startDiscoveryWorkers(t *testing.T, c *crawler) {
	t.Helper()
	for i := range 2 {
		w := discoveryWorker{
			id:      i,
			crawler: c,
		}
		go w.discover(t.Context())
	}
}

func makeDiscoveredNode(id byte, port int) *Node {
	// Derive a BEP-42 compliant node ID for 127.0.0.1. BEP-42 constrains the
	// top 21 bits and bottom 3 bits only; bytes 3–18 are free, so we write id
	// there to give each test node a distinct identity.
	nodeID, err := DeriveNodeIDFromIP(net.ParseIP("127.0.0.1"))
	if err != nil {
		panic(err)
	}
	nodeID[3] = id
	return &Node{
		ID:       nodeID,
		Addr:     &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port},
		LastSeen: time.Now(),
	}
}

// newGetPeersValueServer starts a minimal UDP listener that responds to any
// incoming query with a get_peers Values response containing peer.
func newGetPeersValueServer(t *testing.T, peer *net.UDPAddr) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	udpConn := conn.(*net.UDPConn)
	t.Cleanup(func() { udpConn.Close() })

	var fakeID NodeID
	fakeID[0] = 0x42

	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := udpConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			var req Msg
			if err := bencode.Unmarshal(buf[:n], &req); err != nil {
				continue
			}
			resp := Msg{
				T: req.T,
				Y: "r",
				R: &Return{
					ID:     string(fakeID[:]),
					Values: []string{EncodePeer(peer.IP, peer.Port)},
				},
			}
			data, err := bencode.Marshal(resp)
			if err != nil {
				continue
			}
			udpConn.WriteToUDP(data, addr) //nolint:errcheck
		}
	}()

	return udpConn
}

func TestNewCrawler(t *testing.T) {
	t.Run("dedup is wired into server", func(t *testing.T) {
		c := makeCrawler(t)
		assert.NotNil(t, c.dedup)
		assert.Same(t, c.dedup, c.server.dedup)
	})

	t.Run("discovery channel is shared with server", func(t *testing.T) {
		c := makeCrawler(t)
		assert.Equal(t, c.discovered, c.server.discovered)
	})

}


func TestBep51PQ_ordering(t *testing.T) {
	t.Run("pop returns closest item first", func(t *testing.T) {
		var target NodeID

		var nearID NodeID
		nearID[0] = 0x01
		near := &traversalItem{node: makeDiscoveredNode(0x01, 1001), target: target, dist: target.XOR(nearID)}

		var farID NodeID
		farID[0] = 0xff
		far := &traversalItem{node: makeDiscoveredNode(0xff, 1002), target: target, dist: target.XOR(farID)}

		pq := &traversalHeap{}
		heap.Init(pq)
		heap.Push(pq, far)
		heap.Push(pq, near)

		first := heap.Pop(pq).(*traversalItem)
		assert.Equal(t, near.dist, first.dist)
	})

	t.Run("maintains heap invariant across multiple pushes", func(t *testing.T) {
		var target NodeID
		pq := &traversalHeap{}
		heap.Init(pq)

		for _, b := range []byte{0x50, 0x10, 0xff, 0x01, 0x80} {
			var id NodeID
			id[0] = b
			heap.Push(pq, &traversalItem{
				node:   makeDiscoveredNode(b, int(b)+1000),
				target: target,
				dist:   target.XOR(id),
			})
		}

		prev := heap.Pop(pq).(*traversalItem)
		for pq.Len() > 0 {
			next := heap.Pop(pq).(*traversalItem)
			assert.LessOrEqual(t, prev.dist[0], next.dist[0])
			prev = next
		}
	})
}

func TestCrawler_nextEligible(t *testing.T) {
	t.Run("returns nil for empty queues", func(t *testing.T) {
		c := makeCrawler(t)
		w := crawlerInstance{
			crawler:  c,
			ready:    make(traversalHeap, 0),
			cooldown: make(cooldownHeap, 0),
			seen:     make(map[NodeID]time.Time),
		}
		heap.Init(&w.ready)
		heap.Init(&w.cooldown)
		assert.Nil(t, w.nextEligible(time.Now()))
	})

	t.Run("skips nodes within cooldown", func(t *testing.T) {
		var target NodeID
		node := makeDiscoveredNode(0x01, 1001)
		c := makeCrawler(t)
		nextAllowed := time.Now().Add(time.Hour)
		item := &traversalItem{node: node, target: target, dist: target.XOR(node.ID)}
		w := crawlerInstance{
			crawler:  c,
			ready:    make(traversalHeap, 0),
			cooldown: make(cooldownHeap, 0),
			seen:     map[NodeID]time.Time{node.ID: nextAllowed},
		}
		heap.Init(&w.ready)
		heap.Init(&w.cooldown)
		heap.Push(&w.cooldown, &cooldownItem{item: item, nextAllowed: nextAllowed})
		assert.Nil(t, w.nextEligible(time.Now()))
	})

	t.Run("returns node whose cooldown has expired", func(t *testing.T) {
		var target NodeID
		node := makeDiscoveredNode(0x01, 1001)
		c := makeCrawler(t)
		nextAllowed := time.Now().Add(-time.Second)
		item := &traversalItem{node: node, target: target, dist: target.XOR(node.ID)}
		w := crawlerInstance{
			crawler:  c,
			ready:    make(traversalHeap, 0),
			cooldown: make(cooldownHeap, 0),
			seen:     map[NodeID]time.Time{node.ID: nextAllowed},
		}
		heap.Init(&w.ready)
		heap.Init(&w.cooldown)
		heap.Push(&w.cooldown, &cooldownItem{item: item, nextAllowed: nextAllowed})

		got := w.nextEligible(time.Now())
		require.NotNil(t, got)
		assert.Equal(t, node.ID, got.node.ID)
	})

	t.Run("returns unseen node immediately", func(t *testing.T) {
		c := makeCrawler(t)
		w := crawlerInstance{
			crawler:  c,
			ready:    make(traversalHeap, 0),
			cooldown: make(cooldownHeap, 0),
			seen:     make(map[NodeID]time.Time),
		}

		var target NodeID
		node := makeDiscoveredNode(0x02, 1002)

		heap.Init(&w.ready)
		heap.Init(&w.cooldown)
		heap.Push(&w.ready, &traversalItem{node: node, target: target, dist: target.XOR(node.ID)})

		got := w.nextEligible(time.Now())
		require.NotNil(t, got)
		assert.Equal(t, node.ID, got.node.ID)
	})

	t.Run("retains node in seen after pop so in-flight node acts as dedup guard", func(t *testing.T) {
		var target NodeID
		node := makeDiscoveredNode(0x01, 1001)
		c := makeCrawler(t)
		nextAllowed := time.Now().Add(-time.Second)
		item := &traversalItem{node: node, target: target, dist: target.XOR(node.ID)}
		w := crawlerInstance{
			crawler:  c,
			ready:    make(traversalHeap, 0),
			cooldown: make(cooldownHeap, 0),
			seen:     map[NodeID]time.Time{node.ID: nextAllowed},
		}
		heap.Init(&w.ready)
		heap.Init(&w.cooldown)
		heap.Push(&w.cooldown, &cooldownItem{item: item, nextAllowed: nextAllowed})

		got := w.nextEligible(time.Now())
		require.NotNil(t, got)
		assert.Equal(t, node.ID, got.node.ID)
		_, stillInSeen := w.seen[node.ID]
		assert.True(t, stillInSeen, "node must remain in seen while in-flight so seedQueue/processNodes cannot re-insert it")
	})
}

func TestCrawler_processSamples(t *testing.T) {
	t.Run("forwards new infohashes to discovery channel", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		c := makeCrawler(t)
		startDiscoveryWorkers(t, c)
		require.NoError(t, c.server.Start(ctx))

		peerAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9876}
		responder := newGetPeersValueServer(t, peerAddr)
		node := makeDiscoveredNode(0x01, responder.LocalAddr().(*net.UDPAddr).Port)
		item := &traversalItem{node: node}

		var h [20]byte
		h[0] = 0xAB

		w := crawlerInstance{
			crawler: c,
		}
		w.processSamples(ctx, string(h[:]), item)

		select {
		case event := <-c.discovered:
			assert.Equal(t, h, event.Infohash)
			assert.Equal(t, 1, len(event.Peers))
			assert.Equal(t, peerAddr.Port, event.Peers[0].Port)
		case <-time.After(2 * time.Second):
			t.Fatal("expected discovery event")
		}
	})

	t.Run("deduplicates repeated infohashes", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		c := makeCrawler(t)
		startDiscoveryWorkers(t, c)
		require.NoError(t, c.server.Start(ctx))

		peerAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9877}
		responder := newGetPeersValueServer(t, peerAddr)
		node := makeDiscoveredNode(0x01, responder.LocalAddr().(*net.UDPAddr).Port)
		item := &traversalItem{node: node}

		var h [20]byte
		h[0] = 0xCD

		w := crawlerInstance{
			crawler: c,
		}
		w.processSamples(ctx, string(h[:]), item)
		w.processSamples(ctx, string(h[:]), item)

		select {
		case <-c.discovered:
		case <-time.After(2 * time.Second):
			t.Fatal("expected first discovery event")
		}
		select {
		case <-c.discovered:
			t.Fatal("duplicate should be filtered by dedup")
		case <-time.After(50 * time.Millisecond):
		}
	})

	t.Run("ignores partial trailing bytes", func(t *testing.T) {
		c := makeCrawler(t)
		node := makeDiscoveredNode(0x01, 1001)
		item := &traversalItem{node: node}

		w := crawlerInstance{
			crawler: c,
		}
		w.processSamples(context.Background(), string(make([]byte, 19)), item)

		select {
		case <-c.discovered:
			t.Fatal("partial bytes should not produce a discovery event")
		case <-time.After(50 * time.Millisecond):
		}
	})

	t.Run("drops silently when discovery queue is full", func(t *testing.T) {
		c := makeCrawler(t)
		node := makeDiscoveredNode(0x01, 1001)
		item := &traversalItem{node: node}

		for i := range cap(c.discoveryQueue) {
			c.discoveryQueue <- discoveryWork{infohash: [20]byte{byte(i)}}
		}

		var h [20]byte
		h[0] = 0xFF
		h[1] = 0xFF
		w := crawlerInstance{
			crawler: c,
		}

		w.processSamples(context.Background(), string(h[:]), item) // must not block
	})

	t.Run("decodes multiple hashes from one samples string", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		c := makeCrawler(t)
		startDiscoveryWorkers(t, c)
		require.NoError(t, c.server.Start(ctx))

		peerAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9878}
		responder := newGetPeersValueServer(t, peerAddr)
		node := makeDiscoveredNode(0x01, responder.LocalAddr().(*net.UDPAddr).Port)
		item := &traversalItem{node: node}

		var buf [60]byte
		buf[0] = 0x01
		buf[20] = 0x02
		buf[40] = 0x03
		w := crawlerInstance{
			crawler: c,
		}
		w.processSamples(ctx, string(buf[:]), item)

		count := 0
		timeout := time.After(2 * time.Second)
		for count < 3 {
			select {
			case <-c.discovered:
				count++
			case <-timeout:
				t.Fatalf("expected 3 events, got %d", count)
			}
		}
	})
}

func TestCrawler_processNodes(t *testing.T) {
	t.Run("inserts decoded nodes into routing table and queue", func(t *testing.T) {
		w := crawlerInstance{
			crawler:  makeCrawler(t),
			ready:    make(traversalHeap, 0),
			cooldown: make(cooldownHeap, 0),
			seen:     make(map[NodeID]time.Time),
		}
		var target NodeID

		node := makeDiscoveredNode(0x10, 2000)
		encoded := EncodeNodes([]*Node{node})

		heap.Init(&w.ready)
		heap.Init(&w.cooldown)
		w.processNodes(context.Background(), encoded, target)

		assert.Equal(t, 1, w.ready.Len())
		item := heap.Pop(&w.ready).(*traversalItem)
		assert.Equal(t, node.ID, item.node.ID)
		assert.Equal(t, target.XOR(node.ID), item.dist)
	})

	t.Run("ignores invalid encoded input", func(t *testing.T) {
		w := crawlerInstance{
			crawler:  makeCrawler(t),
			ready:    make(traversalHeap, 0),
			cooldown: make(cooldownHeap, 0),
			seen:     make(map[NodeID]time.Time),
		}
		var target NodeID

		heap.Init(&w.ready)
		heap.Init(&w.cooldown)
		w.processNodes(context.Background(), "not-valid-compact-nodes", target)
		assert.Equal(t, 0, w.ready.Len())
	})

	t.Run("does not re-insert node that is currently in-flight", func(t *testing.T) {
		// Simulate the state after nextEligible pops a node: the node is absent
		// from both heaps but its seen entry is retained (stale, already-expired
		// time). If processNodes receives that same node in a response it must
		// skip it, otherwise the node ends up in both ready and cooldown.
		node := makeDiscoveredNode(0x10, 2000)
		var target NodeID
		w := crawlerInstance{
			crawler:  makeCrawler(t),
			ready:    make(traversalHeap, 0),
			cooldown: make(cooldownHeap, 0),
			seen:     map[NodeID]time.Time{node.ID: time.Now().Add(-time.Second)},
		}
		heap.Init(&w.ready)
		heap.Init(&w.cooldown)

		encoded := EncodeNodes([]*Node{node})
		w.processNodes(context.Background(), encoded, target)

		assert.Equal(t, 0, w.ready.Len(), "in-flight node must not be pushed to ready heap")
	})

	t.Run("trims ready heap to traversalWidth after processing", func(t *testing.T) {
		cr := makeCrawler(t)
		cr.traversalWidth = 2
		w := crawlerInstance{
			crawler:  cr,
			ready:    make(traversalHeap, 0),
			cooldown: make(cooldownHeap, 0),
			seen:     make(map[NodeID]time.Time),
		}
		heap.Init(&w.ready)

		var target NodeID
		nodes := []*Node{
			makeDiscoveredNode(0x10, 2000),
			makeDiscoveredNode(0x20, 2001),
			makeDiscoveredNode(0x30, 2002),
			makeDiscoveredNode(0x40, 2003),
		}
		w.processNodes(context.Background(), EncodeNodes(nodes), target)

		assert.Equal(t, 2, w.ready.Len(), "ready heap must not exceed traversalWidth")
	})

	t.Run("ready heap never exceeds traversalWidth regardless of how many nodes arrive", func(t *testing.T) {
		// DeriveNodeIDFromIP uses a random r each call so XOR distances are
		// non-deterministic; this test only asserts the bound, not which nodes win.
		cr := makeCrawler(t)
		cr.traversalWidth = 2
		w := crawlerInstance{
			crawler:  cr,
			ready:    make(traversalHeap, 0),
			cooldown: make(cooldownHeap, 0),
			seen:     make(map[NodeID]time.Time),
		}
		heap.Init(&w.ready)

		var target NodeID
		// Pre-load with 2 nodes, then deliver 4 more — heap must stay at 2.
		for _, b := range []byte{0xfe, 0xff} {
			n := makeDiscoveredNode(b, int(b)+3000)
			heap.Push(&w.ready, &traversalItem{node: n, target: target, dist: target.XOR(n.ID)})
		}
		require.Equal(t, 2, w.ready.Len())

		more := []*Node{
			makeDiscoveredNode(0x01, 4000),
			makeDiscoveredNode(0x02, 4001),
			makeDiscoveredNode(0x03, 4002),
			makeDiscoveredNode(0x04, 4003),
		}
		w.processNodes(context.Background(), EncodeNodes(more), target)

		assert.Equal(t, 2, w.ready.Len(), "ready heap must never exceed traversalWidth")
	})
}

// makeItem builds a traversalItem with an explicit first-byte distance so that
// ordering in tests is fully deterministic regardless of BEP-42 randomness.
func makeItem(distByte byte) *traversalItem {
	var d NodeID
	d[0] = distByte
	return &traversalItem{
		node: &Node{ID: NodeID{distByte}},
		dist: d,
	}
}

func TestCrawler_trimReadyToK(t *testing.T) {
	t.Run("does nothing when heap is at or below traversalWidth", func(t *testing.T) {
		cr := makeCrawler(t)
		cr.traversalWidth = 3
		w := crawlerInstance{
			crawler:  cr,
			ready:    make(traversalHeap, 0),
			cooldown: make(cooldownHeap, 0),
			seen:     make(map[NodeID]time.Time),
		}
		heap.Init(&w.ready)

		for _, b := range []byte{0x10, 0x20, 0x30} {
			heap.Push(&w.ready, makeItem(b))
		}

		w.trimReadyToK()

		assert.Equal(t, 3, w.ready.Len())
	})

	t.Run("trims to traversalWidth keeping the k closest nodes", func(t *testing.T) {
		cr := makeCrawler(t)
		cr.traversalWidth = 2
		w := crawlerInstance{
			crawler:  cr,
			ready:    make(traversalHeap, 0),
			cooldown: make(cooldownHeap, 0),
			seen:     make(map[NodeID]time.Time),
		}
		heap.Init(&w.ready)

		// dist[0]: 0x01 < 0x10 < 0x80 < 0xff — explicit and deterministic.
		items := []*traversalItem{makeItem(0x01), makeItem(0x10), makeItem(0x80), makeItem(0xff)}
		for _, it := range items {
			heap.Push(&w.ready, it)
		}
		require.Equal(t, 4, w.ready.Len())

		w.trimReadyToK()

		require.Equal(t, 2, w.ready.Len())
		first := heap.Pop(&w.ready).(*traversalItem)
		second := heap.Pop(&w.ready).(*traversalItem)
		assert.Equal(t, byte(0x01), first.dist[0], "closest node must be kept")
		assert.Equal(t, byte(0x10), second.dist[0], "second closest must be kept")
	})

	t.Run("closer nodes displace farther nodes already in heap", func(t *testing.T) {
		cr := makeCrawler(t)
		cr.traversalWidth = 2
		w := crawlerInstance{
			crawler:  cr,
			ready:    make(traversalHeap, 0),
			cooldown: make(cooldownHeap, 0),
			seen:     make(map[NodeID]time.Time),
		}
		heap.Init(&w.ready)

		heap.Push(&w.ready, makeItem(0x80))
		heap.Push(&w.ready, makeItem(0xff))
		require.Equal(t, 2, w.ready.Len())

		// Push two closer items and trim.
		heap.Push(&w.ready, makeItem(0x01))
		heap.Push(&w.ready, makeItem(0x02))
		w.trimReadyToK()

		require.Equal(t, 2, w.ready.Len())
		first := heap.Pop(&w.ready).(*traversalItem)
		second := heap.Pop(&w.ready).(*traversalItem)
		assert.Equal(t, byte(0x01), first.dist[0], "closer node must survive")
		assert.Equal(t, byte(0x02), second.dist[0], "closer node must survive")
	})

	t.Run("heap min-order is preserved after trim", func(t *testing.T) {
		cr := makeCrawler(t)
		cr.traversalWidth = 3
		w := crawlerInstance{
			crawler:  cr,
			ready:    make(traversalHeap, 0),
			cooldown: make(cooldownHeap, 0),
			seen:     make(map[NodeID]time.Time),
		}
		heap.Init(&w.ready)

		for _, b := range []byte{0x50, 0x10, 0xff, 0x01, 0x80} {
			heap.Push(&w.ready, makeItem(b))
		}

		w.trimReadyToK()
		require.Equal(t, 3, w.ready.Len())

		prev := heap.Pop(&w.ready).(*traversalItem)
		for w.ready.Len() > 0 {
			next := heap.Pop(&w.ready).(*traversalItem)
			assert.LessOrEqual(t, prev.dist[0], next.dist[0], "heap must pop in ascending distance order after trim")
			prev = next
		}
	})
}

func TestCrawler_seedQueue(t *testing.T) {
	t.Run("pushes closest routing table nodes onto queue", func(t *testing.T) {
		w := crawlerInstance{
			crawler:  makeCrawler(t),
			ready:    make(traversalHeap, 0),
			cooldown: make(cooldownHeap, 0),
			seen:     make(map[NodeID]time.Time),
		}

		var target NodeID
		target[0] = 0x50

		node := makeDiscoveredNode(0x10, 2000)
		w.server.table.Insert(node)

		heap.Init(&w.ready)
		heap.Init(&w.cooldown)
		w.seedQueue(target)

		assert.Equal(t, 1, w.ready.Len())
		item := heap.Pop(&w.ready).(*traversalItem)
		assert.Equal(t, target, item.target)
		assert.Equal(t, target.XOR(item.node.ID), item.dist)
	})
}

func TestCrawler_seedQueue_inflight(t *testing.T) {
	t.Run("does not re-insert node that is currently in-flight", func(t *testing.T) {
		// Same scenario: node was popped by nextEligible, seen entry is stale but
		// present. A concurrent seedQueue call that happens to pull the same node
		// from the routing table must not push it to ready.
		node := makeDiscoveredNode(0x10, 2000)
		var target NodeID
		w := crawlerInstance{
			crawler:  makeCrawler(t),
			ready:    make(traversalHeap, 0),
			cooldown: make(cooldownHeap, 0),
			seen:     map[NodeID]time.Time{node.ID: time.Now().Add(-time.Second)},
		}
		w.server.table.Insert(node)
		heap.Init(&w.ready)
		heap.Init(&w.cooldown)

		w.seedQueue(target)

		assert.Equal(t, 0, w.ready.Len(), "in-flight node must not be pushed to ready heap by seedQueue")
	})
}

func TestCrawler_mergeNodes(t *testing.T) {
	t.Run("merges two node maps", func(t *testing.T) {
		c := makeCrawler(t)

		node1 := makeDiscoveredNode(0x10, 2000)
		node2 := makeDiscoveredNode(0x20, 2001)
		node3 := makeDiscoveredNode(0x30, 2002)

		shortlist := map[NodeID]*Node{
			node1.ID: node1,
			node2.ID: node2,
		}

		newNodes := map[NodeID]*Node{
			node2.ID: node2,
			node3.ID: node3,
		}

		result := c.mergeNodes(shortlist, newNodes)

		expected := map[NodeID]*Node{
			node1.ID: node1,
			node2.ID: node2,
			node3.ID: node3,
		}
		assert.Equal(t, expected, result)
	})

	t.Run("returns new map without modifying inputs", func(t *testing.T) {
		c := makeCrawler(t)

		node1 := makeDiscoveredNode(0x10, 2000)
		node2 := makeDiscoveredNode(0x20, 2001)

		shortlist := map[NodeID]*Node{node1.ID: node1}
		newNodes := map[NodeID]*Node{node2.ID: node2}

		result := c.mergeNodes(shortlist, newNodes)

		assert.Equal(t, map[NodeID]*Node{node1.ID: node1}, shortlist)
		assert.Equal(t, map[NodeID]*Node{node2.ID: node2}, newNodes)
		assert.Equal(t, map[NodeID]*Node{node1.ID: node1, node2.ID: node2}, result)
	})

	t.Run("handles empty maps", func(t *testing.T) {
		c := makeCrawler(t)

		node1 := makeDiscoveredNode(0x10, 2000)
		shortlist := map[NodeID]*Node{}
		newNodes := map[NodeID]*Node{node1.ID: node1}

		result := c.mergeNodes(shortlist, newNodes)

		assert.Equal(t, map[NodeID]*Node{node1.ID: node1}, result)
	})
}

func TestCrawler_extractNodes(t *testing.T) {
	t.Run("extracts nodes from response and inserts into routing table", func(t *testing.T) {
		c := makeCrawler(t)

		node := makeDiscoveredNode(0x10, 2000)
		encoded := EncodeNodes([]*Node{node})

		resp := &Msg{
			R: &Return{
				Nodes: encoded,
			},
		}

		result := c.extractNodes(resp)

		assert.Equal(t, 1, len(result))
		assert.Equal(t, node.ID, result[node.ID].ID)
		assert.Equal(t, 2000, result[node.ID].Addr.Port)
		assert.Equal(t, "127.0.0.1", result[node.ID].Addr.IP.String())
	})

	t.Run("returns nil when no nodes in response", func(t *testing.T) {
		c := makeCrawler(t)

		resp := &Msg{
			R: &Return{
				Nodes: "",
			},
		}

		result := c.extractNodes(resp)

		assert.Nil(t, result)
	})

	t.Run("returns nil on decode error", func(t *testing.T) {
		c := makeCrawler(t)

		resp := &Msg{
			R: &Return{
				Nodes: "invalid",
			},
		}

		result := c.extractNodes(resp)

		assert.Nil(t, result)
	})

	t.Run("extracts multiple nodes", func(t *testing.T) {
		c := makeCrawler(t)

		node1 := makeDiscoveredNode(0x10, 2000)
		node2 := makeDiscoveredNode(0x20, 2001)
		node3 := makeDiscoveredNode(0x30, 2002)
		encoded := EncodeNodes([]*Node{node1, node2, node3})

		resp := &Msg{
			R: &Return{
				Nodes: encoded,
			},
		}

		result := c.extractNodes(resp)

		assert.Equal(t, 3, len(result))
		assert.Equal(t, node1.ID, result[node1.ID].ID)
		assert.Equal(t, node2.ID, result[node2.ID].ID)
		assert.Equal(t, node3.ID, result[node3.ID].ID)
	})
}

func TestCrawler_processResponses(t *testing.T) {
	t.Run("returns true when peers found", func(t *testing.T) {
		c := makeCrawler(t)

		peer := &net.TCPAddr{IP: net.ParseIP("1.2.3.4"), Port: 6881}
		encodedPeer := EncodePeer(peer.IP, peer.Port)

		var h [20]byte
		h[0] = 0xab

		resp := &Msg{
			R: &Return{
				Values: []string{encodedPeer},
			},
		}

		foundPeers, newNodes := c.processResponses(context.Background(), []*Msg{resp}, h)

		assert.Equal(t, true, foundPeers)
		assert.Nil(t, newNodes)
	})

	t.Run("returns nodes when no peers found", func(t *testing.T) {
		c := makeCrawler(t)

		node := makeDiscoveredNode(0x10, 2000)
		encodedNodes := EncodeNodes([]*Node{node})

		var h [20]byte
		h[0] = 0xab

		resp := &Msg{
			R: &Return{
				Nodes: encodedNodes,
			},
		}

		foundPeers, gotNodes := c.processResponses(context.Background(), []*Msg{resp}, h)

		decodedNodes, _ := DecodeNodes(encodedNodes)
		wantNodes := make(map[NodeID]*Node)
		for _, n := range decodedNodes {
			wantNodes[n.ID] = n
		}
		assert.Equal(t, false, foundPeers)
		assert.Equal(t, wantNodes, gotNodes)
	})

	t.Run("ignores nil responses", func(t *testing.T) {
		c := makeCrawler(t)

		var h [20]byte

		foundPeers, newNodes := c.processResponses(context.Background(), []*Msg{nil, nil}, h)

		assert.Equal(t, false, foundPeers)
		assert.Nil(t, newNodes)
	})

	t.Run("ignores responses without R", func(t *testing.T) {
		c := makeCrawler(t)

		var h [20]byte

		foundPeers, newNodes := c.processResponses(context.Background(), []*Msg{{Y: "r"}}, h)

		assert.Equal(t, false, foundPeers)
		assert.Nil(t, newNodes)
	})

	t.Run("aggregates nodes from multiple responses", func(t *testing.T) {
		c := makeCrawler(t)

		node1 := makeDiscoveredNode(0x10, 2000)
		node2 := makeDiscoveredNode(0x20, 2001)
		encodedNodes1 := EncodeNodes([]*Node{node1})
		encodedNodes2 := EncodeNodes([]*Node{node2})

		var h [20]byte
		h[0] = 0xab

		resp1 := &Msg{R: &Return{Nodes: encodedNodes1}}
		resp2 := &Msg{R: &Return{Nodes: encodedNodes2}}

		foundPeers, gotNodes := c.processResponses(context.Background(), []*Msg{resp1, resp2}, h)

		decodedNodes, _ := DecodeNodes(encodedNodes1 + encodedNodes2)
		wantNodes := make(map[NodeID]*Node)
		for _, n := range decodedNodes {
			wantNodes[n.ID] = n
		}
		assert.Equal(t, false, foundPeers)
		assert.Equal(t, wantNodes, gotNodes)
	})

	t.Run("returns true and no nodes when peers found", func(t *testing.T) {
		c := makeCrawler(t)

		peer := &net.TCPAddr{IP: net.ParseIP("1.2.3.4"), Port: 6881}
		encodedPeer := EncodePeer(peer.IP, peer.Port)

		var h [20]byte
		h[0] = 0xab

		respWithPeers := &Msg{R: &Return{Values: []string{encodedPeer}}}

		foundPeers, gotNodes := c.processResponses(context.Background(), []*Msg{respWithPeers}, h)

		assert.Equal(t, true, foundPeers)
		assert.Nil(t, gotNodes)
	})
}


func TestComputeInterval(t *testing.T) {
	const floor = 10 * time.Second
	const cap5s = 5 * time.Second

	makeC := func(maxInterval time.Duration) *crawler {
		cr := &crawler{
			defaultInterval: floor,
			maxInterval:     maxInterval,
		}
		return cr
	}

	t.Run("nil response returns floor", func(t *testing.T) {
		assert.Equal(t, floor, makeC(0).computeInterval(nil))
	})

	t.Run("nil R field returns floor", func(t *testing.T) {
		assert.Equal(t, floor, makeC(0).computeInterval(&Msg{}))
	})

	t.Run("zero interval returns floor", func(t *testing.T) {
		assert.Equal(t, floor, makeC(0).computeInterval(&Msg{R: &Return{Interval: 0}}))
	})

	t.Run("interval below floor is clamped to floor", func(t *testing.T) {
		assert.Equal(t, floor, makeC(0).computeInterval(&Msg{R: &Return{Interval: 3}}))
	})

	t.Run("interval above floor with no cap is returned as-is", func(t *testing.T) {
		assert.Equal(t, 30*time.Second, makeC(0).computeInterval(&Msg{R: &Return{Interval: 30}}))
	})

	t.Run("interval above cap is clamped to cap", func(t *testing.T) {
		assert.Equal(t, cap5s, makeC(cap5s).computeInterval(&Msg{R: &Return{Interval: 30}}))
	})

	t.Run("interval equal to cap is returned unchanged", func(t *testing.T) {
		// floor=2s, cap=5s, interval=5s → exactly at the cap, returned as-is
		c := &crawler{defaultInterval: 2 * time.Second, maxInterval: cap5s}
		assert.Equal(t, cap5s, c.computeInterval(&Msg{R: &Return{Interval: 5}}))
	})

	t.Run("interval between floor and cap is returned as-is", func(t *testing.T) {
		// floor=10s, cap=30s, interval=20s → 20s
		assert.Equal(t, 20*time.Second, makeC(30*time.Second).computeInterval(&Msg{R: &Return{Interval: 20}}))
	})
}

func TestCrawler_pruneStaleInFlight(t *testing.T) {
	t.Run("removes entries older than 2x transactionTimeout", func(t *testing.T) {
		cr := makeCrawler(t)
		w := crawlerInstance{
			crawler: cr,
			seen:    make(map[NodeID]time.Time),
		}

		// transactionTimeout=2s → cutoff = now-4s; entries at now-10s are stale.
		var staleID NodeID
		staleID[0] = 0x01
		w.seen[staleID] = time.Now().Add(-10 * time.Second)

		// Entry within the window — must survive.
		var freshID NodeID
		freshID[0] = 0x02
		w.seen[freshID] = time.Now().Add(-time.Second)

		w.pruneStaleInFlight()

		_, staleGone := w.seen[staleID]
		assert.False(t, staleGone, "stale entry must be evicted")
		_, freshPresent := w.seen[freshID]
		assert.True(t, freshPresent, "recent entry must be kept")
	})

	t.Run("caps seen map at 4x traversalWidth by evicting past-timed entries", func(t *testing.T) {
		cr := makeCrawler(t)
		cr.traversalWidth = 2 // maxSize = 8
		w := crawlerInstance{
			crawler: cr,
			seen:    make(map[NodeID]time.Time),
		}

		// 9 entries at now-1s: not stale (within 4s window) but exceed maxSize=8.
		past := time.Now().Add(-time.Second)
		for i := range 9 {
			var id NodeID
			id[0] = byte(i + 1)
			w.seen[id] = past
		}
		require.Equal(t, 9, len(w.seen))

		w.pruneStaleInFlight()

		assert.LessOrEqual(t, len(w.seen), 4*cr.traversalWidth)
	})

	t.Run("size cap preserves future-timed cooldown entries", func(t *testing.T) {
		cr := makeCrawler(t)
		cr.traversalWidth = 2 // maxSize = 8
		w := crawlerInstance{
			crawler: cr,
			seen:    make(map[NodeID]time.Time),
		}

		// 8 future-timed entries (active cooldowns) + 1 past-timed = 9 total.
		future := time.Now().Add(time.Hour)
		var futureIDs [8]NodeID
		for i := range 8 {
			futureIDs[i][0] = byte(i + 1)
			w.seen[futureIDs[i]] = future
		}
		var pastID NodeID
		pastID[19] = 0xFF
		w.seen[pastID] = time.Now().Add(-time.Second)

		w.pruneStaleInFlight()

		_, pastEvicted := w.seen[pastID]
		assert.False(t, pastEvicted, "past-timed in-flight sentinel must be evicted to meet size cap")
		for i, id := range futureIDs {
			_, present := w.seen[id]
			assert.True(t, present, "future-timed cooldown entry %d must be preserved", i)
		}
	})

	t.Run("no-op when seen is within size cap", func(t *testing.T) {
		cr := makeCrawler(t)
		cr.traversalWidth = 4 // maxSize = 16
		w := crawlerInstance{
			crawler: cr,
			seen:    make(map[NodeID]time.Time),
		}

		future := time.Now().Add(time.Hour)
		for i := range 5 {
			var id NodeID
			id[0] = byte(i + 1)
			w.seen[id] = future
		}

		w.pruneStaleInFlight()

		assert.Equal(t, 5, len(w.seen))
	})
}
