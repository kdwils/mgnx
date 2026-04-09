package dht

import (
	"container/heap"
	"context"
	"net"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/kdwils/mgnx/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeCrawler(t *testing.T) *crawler {
	t.Helper()
	cr, err := NewCrawler(config.Crawler{Crawlers: 2, Alpha: 3, MaxIterations: 4}, testServerCfg(t))
	require.NoError(t, err)
	cr.discoveryQueue = make(chan discoveryWork, 64)
	t.Cleanup(func() { cr.Stop(t.Context()) })
	return cr
}

// startDiscoveryWorkers launches discovery workers for tests that need the full
// processSamples → discoverPeers → discovered pipeline.
func startDiscoveryWorkers(t *testing.T, c *crawler) {
	t.Helper()
	for range 2 {
		c.wg.Go(func() { c.discoveryWorker(t.Context()) })
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
	t.Run("returns nil for empty queue", func(t *testing.T) {
		c := makeCrawler(t)
		pq := &traversalHeap{}
		heap.Init(pq)
		assert.Nil(t, c.nextEligible(pq, make(map[NodeID]time.Time)))
	})

	t.Run("skips nodes within cooldown", func(t *testing.T) {
		c := makeCrawler(t)
		var target NodeID
		node := makeDiscoveredNode(0x01, 1001)
		pq := &traversalHeap{}
		heap.Init(pq)
		heap.Push(pq, &traversalItem{node: node, target: target, dist: target.XOR(node.ID)})

		seen := map[NodeID]time.Time{node.ID: time.Now().Add(time.Hour)}
		assert.Nil(t, c.nextEligible(pq, seen))
	})

	t.Run("returns node whose cooldown has expired", func(t *testing.T) {
		c := makeCrawler(t)
		var target NodeID
		node := makeDiscoveredNode(0x01, 1001)
		pq := &traversalHeap{}
		heap.Init(pq)
		heap.Push(pq, &traversalItem{node: node, target: target, dist: target.XOR(node.ID)})

		seen := map[NodeID]time.Time{node.ID: time.Now().Add(-time.Second)}
		item := c.nextEligible(pq, seen)
		require.NotNil(t, item)
		assert.Equal(t, node.ID, item.node.ID)
	})

	t.Run("returns unseen node immediately", func(t *testing.T) {
		c := makeCrawler(t)
		var target NodeID
		node := makeDiscoveredNode(0x02, 1002)
		pq := &traversalHeap{}
		heap.Init(pq)
		heap.Push(pq, &traversalItem{node: node, target: target, dist: target.XOR(node.ID)})

		item := c.nextEligible(pq, make(map[NodeID]time.Time))
		require.NotNil(t, item)
		assert.Equal(t, node.ID, item.node.ID)
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
		c.processSamples(ctx, string(h[:]), item)

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
		c.processSamples(ctx, string(h[:]), item)
		c.processSamples(ctx, string(h[:]), item)

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

		c.processSamples(context.Background(), string(make([]byte, 19)), item)

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
		c.processSamples(context.Background(), string(h[:]), item) // must not block
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
		c.processSamples(ctx, string(buf[:]), item)

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
		c := makeCrawler(t)
		var target NodeID

		node := makeDiscoveredNode(0x10, 2000)
		encoded := EncodeNodes([]*Node{node})

		pq := &traversalHeap{}
		heap.Init(pq)
		c.processNodes(encoded, target, pq)

		assert.Equal(t, 1, pq.Len())
		item := heap.Pop(pq).(*traversalItem)
		assert.Equal(t, node.ID, item.node.ID)
		assert.Equal(t, target.XOR(node.ID), item.dist)
	})

	t.Run("ignores invalid encoded input", func(t *testing.T) {
		c := makeCrawler(t)
		var target NodeID

		pq := &traversalHeap{}
		heap.Init(pq)
		c.processNodes("bad", target, pq)
		assert.Equal(t, 0, pq.Len())
	})
}

func TestCrawler_seedQueue(t *testing.T) {
	t.Run("pushes closest routing table nodes onto queue", func(t *testing.T) {
		c := makeCrawler(t)

		var target NodeID
		target[0] = 0x50

		node := makeDiscoveredNode(0x10, 2000)
		c.server.table.Insert(node)

		pq := &traversalHeap{}
		heap.Init(pq)
		c.seedQueue(pq, target)

		assert.Equal(t, 1, pq.Len())
		item := heap.Pop(pq).(*traversalItem)
		assert.Equal(t, target, item.target)
		assert.Equal(t, target.XOR(item.node.ID), item.dist)
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
