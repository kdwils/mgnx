package dht

import (
	"container/heap"
	"context"
	"net"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
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

func makeDiscoveredNode(id byte, port int) *Node {
	var nodeID NodeID
	nodeID[0] = id
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
		near := &bep51Item{node: makeDiscoveredNode(0x01, 1001), target: target, dist: target.XOR(nearID)}

		var farID NodeID
		farID[0] = 0xff
		far := &bep51Item{node: makeDiscoveredNode(0xff, 1002), target: target, dist: target.XOR(farID)}

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
				node:   makeDiscoveredNode(b, int(b)+1000),
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
		node := makeDiscoveredNode(0x01, 1001)
		pq := &bep51PQ{}
		heap.Init(pq)
		heap.Push(pq, &bep51Item{node: node, target: target, dist: target.XOR(node.ID)})

		seen := map[NodeID]time.Time{node.ID: time.Now().Add(time.Hour)}
		assert.Nil(t, c.nextEligible(pq, seen))
	})

	t.Run("returns node whose cooldown has expired", func(t *testing.T) {
		c := makeCrawler(t)
		var target NodeID
		node := makeDiscoveredNode(0x01, 1001)
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
		node := makeDiscoveredNode(0x02, 1002)
		pq := &bep51PQ{}
		heap.Init(pq)
		heap.Push(pq, &bep51Item{node: node, target: target, dist: target.XOR(node.ID)})

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
		require.NoError(t, c.server.Start(ctx))

		peerAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9876}
		responder := newGetPeersValueServer(t, peerAddr)
		node := makeDiscoveredNode(0x01, responder.LocalAddr().(*net.UDPAddr).Port)
		item := &bep51Item{node: node}

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
		require.NoError(t, c.server.Start(ctx))

		peerAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9877}
		responder := newGetPeersValueServer(t, peerAddr)
		node := makeDiscoveredNode(0x01, responder.LocalAddr().(*net.UDPAddr).Port)
		item := &bep51Item{node: node}

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
		item := &bep51Item{node: node}

		c.processSamples(context.Background(), string(make([]byte, 19)), item)

		select {
		case <-c.discovered:
			t.Fatal("partial bytes should not produce a discovery event")
		case <-time.After(50 * time.Millisecond):
		}
	})

	t.Run("drops silently when discovery channel is full", func(t *testing.T) {
		c := makeCrawler(t)
		node := makeDiscoveredNode(0x01, 1001)
		item := &bep51Item{node: node}

		for i := range cap(c.discovered) {
			c.discovered <- DiscoveredPeer{Infohash: [20]byte{byte(i)}}
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
		require.NoError(t, c.server.Start(ctx))

		peerAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9878}
		responder := newGetPeersValueServer(t, peerAddr)
		node := makeDiscoveredNode(0x01, responder.LocalAddr().(*net.UDPAddr).Port)
		item := &bep51Item{node: node}

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
			c.server.table.Insert(makeDiscoveredNode(byte(i+1), 3000+i))
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
