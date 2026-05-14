package crawler

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/kdwils/mgnx/config"
	dhtMocks "github.com/kdwils/mgnx/dht/crawler/mocks"
	"github.com/kdwils/mgnx/dht/krpc"
	"github.com/kdwils/mgnx/dht/table"
	"github.com/kdwils/mgnx/dht/types"
	"github.com/kdwils/mgnx/recorder"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
)

func testCrawlerCfg() config.Crawler {
	return config.Crawler{
		Alpha:                  3,
		MaxIterations:          100,
		DiscoveryMaxIterations: 4,
		TraversalWidth:         8,
		DefaultCooldown:        10 * time.Millisecond,
		DefaultInterval:        10 * time.Millisecond,
		MaxNodeFailures:        3,
		MaxJitter:              0,
		EmptySpinWait:          10 * time.Millisecond,
		SampleEnqueueTimeout:   50 * time.Millisecond,
		NodeCacheCleanup:       1 * time.Hour,
		TransactionTimeout:     200 * time.Millisecond,
	}
}

func makeTestNode(id byte, port int) *table.Node {
	ip := net.IP{127, 0, 0, id + 1}
	nodeID, _ := table.DeriveNodeIDFromIP(ip)
	return &table.Node{
		ID:   nodeID,
		Addr: &net.UDPAddr{IP: ip, Port: port},
	}
}

func TestDiscoveryWorker_Start(t *testing.T) {
	t.Run("consumes from queue and performs discovery", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := dhtMocks.NewMockDHT(ctrl)

		node := makeTestNode(0x10, 2000)
		var h [20]byte
		h[0] = 0xAB
		var infoHashID table.NodeID
		copy(infoHashID[:], h[:])

		m.EXPECT().Closest(gomock.Any(), gomock.Any()).Return([]*table.Node{}).AnyTimes()
		m.EXPECT().GetPeers(gomock.Any(), node.Addr, node.ID, infoHashID).
			Return(&krpc.Msg{Y: "r", R: &krpc.Return{ID: string(node.ID[:])}}, nil)

		queue := make(chan DiscoveryWork, 1)
		w := NewDiscoveryWorker(0, m, queue, testCrawlerCfg(), recorder.NewNoOp())

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		queue <- DiscoveryWork{Infohash: h, Source: node}
		w.Start(ctx)
	})

	t.Run("stops when context is cancelled", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := dhtMocks.NewMockDHT(ctrl)

		queue := make(chan DiscoveryWork)
		w := NewDiscoveryWorker(0, m, queue, testCrawlerCfg(), recorder.NewNoOp())

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			w.Start(ctx)
			close(done)
		}()

		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Start did not return after context cancellation")
		}
	})
}

func TestDiscoveryWorker_discoverPeers(t *testing.T) {
	t.Run("emits peers when GetPeers returns values", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := dhtMocks.NewMockDHT(ctrl)

		node := makeTestNode(0x10, 2000)
		peerIP := net.IP{1, 2, 3, 4}
		peerPort := 6881

		var h [20]byte
		h[0] = 0xAB
		var infoHashID table.NodeID
		copy(infoHashID[:], h[:])

		m.EXPECT().
			Closest(gomock.Any(), gomock.Any()).Return([]*table.Node{}).AnyTimes()
		m.EXPECT().
			GetPeers(gomock.Any(), node.Addr, node.ID, infoHashID).
			Return(&krpc.Msg{Y: "r", R: &krpc.Return{
				ID:     string(node.ID[:]),
				Values: []string{table.EncodePeer(peerIP, peerPort)},
			}}, nil)

		m.EXPECT().Emit(gomock.Any()).Do(func(ev types.DiscoveredPeers) {
			assert.Equal(t, h, ev.Infohash)
			assert.Equal(t, []types.PeerAddr{{SourceIP: peerIP, Port: peerPort}}, ev.Peers)
		})

		queue := make(chan DiscoveryWork, 1)
		w := NewDiscoveryWorker(0, m, queue, testCrawlerCfg(), recorder.NewNoOp())

		w.discoverPeers(context.Background(), h, node)
	})

	t.Run("performs iterative lookup when nodes are returned", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := dhtMocks.NewMockDHT(ctrl)

		node1 := makeTestNode(0x10, 2000)
		node2 := makeTestNode(0x20, 2001)
		peerIP := net.IP{5, 6, 7, 8}
		peerPort := 6881

		var h [20]byte
		h[0] = 0xAA
		var infoHashID table.NodeID
		copy(infoHashID[:], h[:])

		m.EXPECT().
			Closest(gomock.Any(), gomock.Any()).Return([]*table.Node{}).AnyTimes()
		m.EXPECT().
			GetPeers(gomock.Any(), node1.Addr, node1.ID, infoHashID).
			Return(&krpc.Msg{Y: "r", R: &krpc.Return{
				ID:    string(node1.ID[:]),
				Nodes: table.EncodeNodes([]*table.Node{node2}),
			}}, nil)

		m.EXPECT().
			GetPeers(gomock.Any(), node2.Addr, node2.ID, infoHashID).
			Return(&krpc.Msg{Y: "r", R: &krpc.Return{
				ID:     string(node2.ID[:]),
				Values: []string{table.EncodePeer(peerIP, peerPort)},
			}}, nil)

		m.EXPECT().InsertNode(gomock.Any(), node2)
		m.EXPECT().Emit(gomock.Any()).Do(func(ev types.DiscoveredPeers) {
			assert.Equal(t, h, ev.Infohash)
			assert.Equal(t, []types.PeerAddr{{SourceIP: peerIP, Port: peerPort}}, ev.Peers)
		})

		queue := make(chan DiscoveryWork, 1)
		w := NewDiscoveryWorker(0, m, queue, testCrawlerCfg(), recorder.NewNoOp())

		w.discoverPeers(context.Background(), h, node1)
	})
}

func TestDiscoveryWorker_trimToKClosest(t *testing.T) {
	node1 := makeTestNode(0x10, 2000)
	node2 := makeTestNode(0x20, 2001)
	node3 := makeTestNode(0x30, 2002)

	var target table.NodeID
	target[0] = 0x25
	nodes := map[table.NodeID]*table.Node{
		node1.ID: node1,
		node2.ID: node2,
		node3.ID: node3,
	}

	cfg := testCrawlerCfg()
	cfg.TraversalWidth = 2
	w := NewDiscoveryWorker(0, nil, nil, cfg, recorder.NewNoOp())

	trimmed := w.trimToKClosest(nodes, target)
	assert.Equal(t, 2, len(trimmed))
	// Verify that every node in trimmed came from the original set.
	for id := range trimmed {
		assert.Contains(t, nodes, id)
	}
}

func TestDiscoveryWorker_sortByDistance(t *testing.T) {
	node1 := makeTestNode(0x10, 2000)
	node2 := makeTestNode(0x20, 2001)
	node3 := makeTestNode(0x30, 2002)

	var target table.NodeID
	target[0] = 0x25
	nodes := map[table.NodeID]*table.Node{
		node1.ID: node1,
		node2.ID: node2,
		node3.ID: node3,
	}

	w := NewDiscoveryWorker(0, nil, nil, testCrawlerCfg(), recorder.NewNoOp())
	sorted := w.sortByDistance(nodes, target)

	assert.Len(t, sorted, 3)
	// Verify sorted order is ascending by XOR distance.
	for i := 1; i < len(sorted); i++ {
		prevDist := sorted[i-1].ID.XOR(target)
		currDist := sorted[i].ID.XOR(target)
		assert.True(t, bytes.Compare(prevDist[:], currDist[:]) <= 0,
			"nodes should be sorted by ascending XOR distance")
	}
}

func TestDiscoveryWorker_processResponses(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := dhtMocks.NewMockDHT(ctrl)

	peerIP := net.IP{1, 1, 1, 1}
	peerPort := 1111
	node := makeTestNode(0x50, 5000)

	responses := []*krpc.Msg{
		{
			Y: "r",
			R: &krpc.Return{
				Values: []string{table.EncodePeer(peerIP, peerPort)},
			},
		},
		{
			Y: "r",
			R: &krpc.Return{
				Nodes: table.EncodeNodes([]*table.Node{node}),
			},
		},
	}

	m.EXPECT().InsertNode(gomock.Any(), node)

	w := NewDiscoveryWorker(0, m, nil, testCrawlerCfg(), recorder.NewNoOp())
	peers, newNodes := w.processResponses(context.Background(), responses)

	assert.Equal(t, []types.PeerAddr{{SourceIP: peerIP, Port: peerPort}}, peers)
	assert.Equal(t, map[table.NodeID]*table.Node{node.ID: node}, newNodes)
}

func TestDiscoveryWorker_extractPeers(t *testing.T) {
	peerIP1 := net.IP{1, 2, 3, 4}
	peerPort1 := 6881
	peerIP2 := net.IP{5, 6, 7, 8}
	peerPort2 := 6882

	resp := &krpc.Msg{
		R: &krpc.Return{
			Values: []string{
				table.EncodePeer(peerIP1, peerPort1),
				table.EncodePeer(peerIP2, peerPort2),
			},
		},
	}

	w := NewDiscoveryWorker(0, nil, nil, testCrawlerCfg(), recorder.NewNoOp())
	peers := w.extractPeers(resp)

	assert.Equal(t, []types.PeerAddr{
		{SourceIP: peerIP1, Port: peerPort1},
		{SourceIP: peerIP2, Port: peerPort2},
	}, peers)
}

func TestDiscoveryWorker_extractNodes(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := dhtMocks.NewMockDHT(ctrl)

	node := makeTestNode(0x10, 2000)
	resp := &krpc.Msg{
		R: &krpc.Return{
			Nodes: table.EncodeNodes([]*table.Node{node}),
		},
	}

	m.EXPECT().InsertNode(gomock.Any(), node)

	w := NewDiscoveryWorker(0, m, nil, testCrawlerCfg(), recorder.NewNoOp())
	nodes := w.extractNodes(context.Background(), resp)

	assert.Equal(t, map[table.NodeID]*table.Node{node.ID: node}, nodes)
}

func TestDiscoveryWorker_mergeNodes(t *testing.T) {
	node1 := makeTestNode(0x10, 2000)
	node2 := makeTestNode(0x20, 2001)

	shortlist := map[table.NodeID]*table.Node{node1.ID: node1}
	newNodes := map[table.NodeID]*table.Node{node2.ID: node2}

	w := NewDiscoveryWorker(0, nil, nil, testCrawlerCfg(), recorder.NewNoOp())
	merged := w.mergeNodes(shortlist, newNodes)

	assert.Equal(t, map[table.NodeID]*table.Node{
		node1.ID: node1,
		node2.ID: node2,
	}, merged)
}
