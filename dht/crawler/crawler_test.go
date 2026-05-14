package crawler

import (
	"container/heap"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/dht/crawler/mocks"
	dhtMocks "github.com/kdwils/mgnx/dht/crawler/mocks"
	"github.com/kdwils/mgnx/dht/filter"
	"github.com/kdwils/mgnx/dht/krpc"
	"github.com/kdwils/mgnx/dht/table"
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

func makeTestNodeID(b byte) table.NodeID {
	var id table.NodeID
	id[0] = b
	return id
}

func makeTestNodeWithID(id table.NodeID, port int) *table.Node {
	ip := net.IP{127, 0, 0, id[0] + 1}
	return &table.Node{
		ID:   id,
		Addr: &net.UDPAddr{IP: ip, Port: port},
	}
}

func TestCrawler_Start(t *testing.T) {
	t.Run("calls GetPeers and SampleInfohashes on seeded nodes", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := mocks.NewMockDHT(ctrl)

		node := makeTestNode(0x10, 2000)

		m.EXPECT().Closest(gomock.Any(), gomock.Any()).Return([]*table.Node{node}).AnyTimes()
		m.EXPECT().NodeCount().Return(1).AnyTimes()
		m.EXPECT().InsertNode(gomock.Any(), node).AnyTimes()
		m.EXPECT().MarkSuccess(node.ID).AnyTimes()
		m.EXPECT().MarkFailure(node.ID).AnyTimes()

		getPeersCalled := make(chan struct{}, 1)
		m.EXPECT().GetPeers(gomock.Any(), node.Addr, node.ID, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ *net.UDPAddr, _ table.NodeID, _ table.NodeID) (*krpc.Msg, error) {
				select {
				case getPeersCalled <- struct{}{}:
				default:
				}
				return &krpc.Msg{Y: "r", R: &krpc.Return{
					ID:    string(node.ID[:]),
					Nodes: table.EncodeNodes([]*table.Node{node}),
				}}, nil
			}).AnyTimes()

		m.EXPECT().SampleInfohashes(gomock.Any(), node.Addr, node.ID, gomock.Any()).
			Return(nil, errors.New("method unknown")).AnyTimes()

		queue := make(chan DiscoveryWork, 8)
		dedup := filter.NewBloomFilter(1_000_000, 0.001, 10*time.Minute)
		c := NewCrawler(0, m, queue, dedup, testCrawlerCfg(), recorder.NewNoOp())

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		go c.Start(ctx)

		select {
		case <-getPeersCalled:
		case <-ctx.Done():
			t.Fatal("GetPeers was never called")
		}
	})

	t.Run("enqueues samples from BEP-51 response", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := dhtMocks.NewMockDHT(ctrl)

		node := makeTestNode(0x10, 2000)

		var sampleHash [20]byte
		sampleHash[0] = 0xDE

		m.EXPECT().Closest(gomock.Any(), gomock.Any()).Return([]*table.Node{node}).AnyTimes()
		m.EXPECT().NodeCount().Return(1).AnyTimes()
		m.EXPECT().InsertNode(gomock.Any(), node).AnyTimes()
		m.EXPECT().MarkSuccess(node.ID).AnyTimes()
		m.EXPECT().MarkFailure(node.ID).AnyTimes()
		m.EXPECT().GetPeers(gomock.Any(), node.Addr, node.ID, gomock.Any()).
			Return(&krpc.Msg{Y: "r", R: &krpc.Return{ID: string(node.ID[:])}}, nil).AnyTimes()
		m.EXPECT().SampleInfohashes(gomock.Any(), node.Addr, node.ID, gomock.Any()).
			Return(&krpc.Msg{Y: "r", R: &krpc.Return{
				ID:      string(node.ID[:]),
				Samples: string(sampleHash[:]),
			}}, nil).AnyTimes()

		queue := make(chan DiscoveryWork, 8)
		dedup := filter.NewBloomFilter(1_000_000, 0.001, 10*time.Minute)
		c := NewCrawler(0, m, queue, dedup, testCrawlerCfg(), recorder.NewNoOp())

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		go c.Start(ctx)

		select {
		case work := <-queue:
			assert.Equal(t, DiscoveryWork{Infohash: sampleHash, Source: node}, work)
		case <-ctx.Done():
			t.Fatal("no work item enqueued from BEP-51 samples")
		}
	})

	t.Run("marks node failed and evicts after MaxNodeFailures", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := dhtMocks.NewMockDHT(ctrl)

		node := makeTestNode(0x10, 2000)

		m.EXPECT().Closest(gomock.Any(), gomock.Any()).Return([]*table.Node{node}).AnyTimes()
		m.EXPECT().NodeCount().Return(1).AnyTimes()
		m.EXPECT().GetPeers(gomock.Any(), node.Addr, node.ID, gomock.Any()).
			Return(nil, errors.New("timeout")).AnyTimes()
		m.EXPECT().SampleInfohashes(gomock.Any(), node.Addr, node.ID, gomock.Any()).
			Return(nil, errors.New("timeout")).AnyTimes()
		m.EXPECT().InsertNode(gomock.Any(), node).AnyTimes()

		failed := make(chan table.NodeID, 16)
		m.EXPECT().MarkFailure(node.ID).DoAndReturn(func(id table.NodeID) {
			select {
			case failed <- id:
			default:
			}
		}).AnyTimes()
		m.EXPECT().MarkSuccess(node.ID).AnyTimes()

		queue := make(chan DiscoveryWork, 8)
		dedup := filter.NewBloomFilter(1_000_000, 0.001, 10*time.Minute)
		c := NewCrawler(0, m, queue, dedup, testCrawlerCfg(), recorder.NewNoOp())

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		go c.Start(ctx)

		select {
		case id := <-failed:
			assert.Equal(t, node.ID, id)
		case <-ctx.Done():
			t.Fatal("MarkFailure was never called")
		}
	})

	t.Run("stops when context is cancelled", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := dhtMocks.NewMockDHT(ctrl)

		m.EXPECT().Closest(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		m.EXPECT().NodeCount().Return(0).AnyTimes()

		queue := make(chan DiscoveryWork, 8)
		dedup := filter.NewBloomFilter(1_000_000, 0.001, 10*time.Minute)
		c := NewCrawler(0, m, queue, dedup, testCrawlerCfg(), recorder.NewNoOp())

		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan struct{})
		go func() {
			c.Start(ctx)
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

func TestCrawler_computeInterval(t *testing.T) {
	defaultInterval := 10 * time.Second
	maxInterval := 60 * time.Second

	tests := []struct {
		name     string
		interval int
		want     time.Duration
	}{
		{
			name:     "zero interval returns default",
			interval: 0,
			want:     defaultInterval,
		},
		{
			name:     "negative interval returns default",
			interval: -1,
			want:     defaultInterval,
		},
		{
			name:     "interval below default is clamped up",
			interval: 2,
			want:     defaultInterval,
		},
		{
			name:     "interval within bounds is kept",
			interval: 30,
			want:     30 * time.Second,
		},
		{
			name:     "interval above max is clamped down",
			interval: 300,
			want:     maxInterval,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testCrawlerCfg()
			cfg.DefaultInterval = defaultInterval
			cfg.MaxInterval = maxInterval
			c := NewCrawler(0, nil, nil, nil, cfg, recorder.NewNoOp())

			got := c.ComputeInterval(tt.interval)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestJitter(t *testing.T) {
	t.Run("zero max returns zero", func(t *testing.T) {
		assert.Equal(t, time.Duration(0), jitter(0))
	})

	t.Run("negative max returns zero", func(t *testing.T) {
		assert.Equal(t, time.Duration(0), jitter(-1*time.Second))
	})

	t.Run("positive max returns value in range", func(t *testing.T) {
		max := 100 * time.Millisecond
		for range 100 {
			j := jitter(max)
			assert.GreaterOrEqual(t, j, time.Duration(0))
			assert.Less(t, j, max)
		}
	})
}

func TestCrawler_queryForSamples(t *testing.T) {
	t.Run("returns cached incapable without querying", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := mocks.NewMockDHT(ctrl)

		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)
		target := makeTestNodeID(0x20)

		m.EXPECT().SampleInfohashes(gomock.Any(), node.Addr, nodeID, target).Times(0)

		cfg := testCrawlerCfg()
		c := NewCrawler(0, m, nil, nil, cfg, recorder.NewNoOp())

		c.nodeSampleSupport.Set(nodeID, supportEntry{capable: false, lastSeen: time.Now()})

		item := &traversalItem{node: node, target: target}
		resp, supported, err := c.queryForSamples(context.Background(), item)

		assert.Nil(t, resp)
		assert.False(t, supported)
		assert.NoError(t, err)
	})

	t.Run("queries and caches capable node", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := mocks.NewMockDHT(ctrl)

		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)
		target := makeTestNodeID(0x20)

		m.EXPECT().SampleInfohashes(gomock.Any(), node.Addr, nodeID, target).
			Return(&krpc.Msg{Y: "r", R: &krpc.Return{ID: string(nodeID[:])}}, nil)

		cfg := testCrawlerCfg()
		c := NewCrawler(0, m, nil, nil, cfg, recorder.NewNoOp())

		item := &traversalItem{node: node, target: target}
		resp, supported, err := c.queryForSamples(context.Background(), item)

		assert.NotNil(t, resp)
		assert.True(t, supported)
		assert.NoError(t, err)

		entry, ok := c.nodeSampleSupport.Get(nodeID)
		assert.True(t, ok)
		assert.True(t, entry.capable)
	})

	t.Run("queries and caches incapable node on 204 error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := mocks.NewMockDHT(ctrl)

		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)
		target := makeTestNodeID(0x20)

		m.EXPECT().SampleInfohashes(gomock.Any(), node.Addr, nodeID, target).
			Return(&krpc.Msg{Y: "e", E: []any{int64(204), "Method Unknown"}}, nil)

		cfg := testCrawlerCfg()
		c := NewCrawler(0, m, nil, nil, cfg, recorder.NewNoOp())

		item := &traversalItem{node: node, target: target}
		resp, supported, err := c.queryForSamples(context.Background(), item)

		assert.Nil(t, resp)
		assert.False(t, supported)
		assert.NoError(t, err)

		entry, ok := c.nodeSampleSupport.Get(nodeID)
		assert.True(t, ok)
		assert.False(t, entry.capable)
	})

	t.Run("returns error on query failure", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := mocks.NewMockDHT(ctrl)

		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)
		target := makeTestNodeID(0x20)

		expectedErr := errors.New("timeout")
		m.EXPECT().SampleInfohashes(gomock.Any(), node.Addr, nodeID, target).
			Return(nil, expectedErr)

		cfg := testCrawlerCfg()
		c := NewCrawler(0, m, nil, nil, cfg, recorder.NewNoOp())

		item := &traversalItem{node: node, target: target}
		resp, supported, err := c.queryForSamples(context.Background(), item)

		assert.Nil(t, resp)
		assert.False(t, supported)
		assert.Equal(t, expectedErr, err)
	})
}

func TestCrawler_promoteReady(t *testing.T) {
	t.Run("promotes expired cooldown items", func(t *testing.T) {
		cfg := testCrawlerCfg()
		c := NewCrawler(0, nil, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.ready)
		heap.Init(&c.cooldown)

		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)
		target := makeTestNodeID(0x20)

		item := &traversalItem{node: node, target: target, dist: target.XOR(nodeID)}
		past := time.Now().Add(-1 * time.Second)
		heap.Push(&c.cooldown, &cooldownItem{item: item, nextAllowed: past})

		c.promoteReady(time.Now())

		assert.Equal(t, 0, c.cooldown.Len())
		assert.Equal(t, 1, c.ready.Len())
	})

	t.Run("does not promote non-expired items", func(t *testing.T) {
		cfg := testCrawlerCfg()
		c := NewCrawler(0, nil, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.ready)
		heap.Init(&c.cooldown)

		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)
		target := makeTestNodeID(0x20)

		item := &traversalItem{node: node, target: target, dist: target.XOR(nodeID)}
		future := time.Now().Add(1 * time.Second)
		heap.Push(&c.cooldown, &cooldownItem{item: item, nextAllowed: future})

		c.promoteReady(time.Now())

		assert.Equal(t, 1, c.cooldown.Len())
		assert.Equal(t, 0, c.ready.Len())
	})

	t.Run("promotes multiple expired items", func(t *testing.T) {
		cfg := testCrawlerCfg()
		c := NewCrawler(0, nil, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.ready)
		heap.Init(&c.cooldown)

		target := makeTestNodeID(0x20)
		for i := 0; i < 5; i++ {
			nodeID := makeTestNodeID(byte(0x10 + i))
			node := makeTestNodeWithID(nodeID, 2000+i)
			item := &traversalItem{node: node, target: target, dist: target.XOR(nodeID)}
			past := time.Now().Add(-1 * time.Second)
			heap.Push(&c.cooldown, &cooldownItem{item: item, nextAllowed: past})
		}

		c.promoteReady(time.Now())

		assert.Equal(t, 0, c.cooldown.Len())
		assert.Equal(t, 5, c.ready.Len())
	})

	t.Run("stops at first non-expired item", func(t *testing.T) {
		cfg := testCrawlerCfg()
		c := NewCrawler(0, nil, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.ready)
		heap.Init(&c.cooldown)

		target := makeTestNodeID(0x20)

		past := time.Now().Add(-1 * time.Second)
		future := time.Now().Add(1 * time.Second)

		nodeID1 := makeTestNodeID(0x10)
		node1 := makeTestNodeWithID(nodeID1, 2000)
		item1 := &traversalItem{node: node1, target: target, dist: target.XOR(nodeID1)}
		heap.Push(&c.cooldown, &cooldownItem{item: item1, nextAllowed: past})

		nodeID2 := makeTestNodeID(0x11)
		node2 := makeTestNodeWithID(nodeID2, 2001)
		item2 := &traversalItem{node: node2, target: target, dist: target.XOR(nodeID2)}
		heap.Push(&c.cooldown, &cooldownItem{item: item2, nextAllowed: future})

		c.promoteReady(time.Now())

		assert.Equal(t, 1, c.cooldown.Len())
		assert.Equal(t, 1, c.ready.Len())
	})
}

func TestCrawler_pushToReady(t *testing.T) {
	t.Run("returns false if node already seen", func(t *testing.T) {
		cfg := testCrawlerCfg()
		c := NewCrawler(0, nil, nil, nil, cfg, recorder.NewNoOp())

		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)
		target := makeTestNodeID(0x20)

		c.seen.Set(nodeID, time.Now())

		assert.False(t, c.pushToReady(node, target))
	})

	t.Run("returns true and pushes to heap for new node", func(t *testing.T) {
		cfg := testCrawlerCfg()
		c := NewCrawler(0, nil, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.ready)

		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)
		target := makeTestNodeID(0x20)

		assert.True(t, c.pushToReady(node, target))
		assert.Equal(t, 1, c.ready.Len())
	})

	t.Run("sets yield factor from nodeSamples", func(t *testing.T) {
		cfg := testCrawlerCfg()
		c := NewCrawler(0, nil, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.ready)

		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)
		target := makeTestNodeID(0x20)

		c.nodeSamples.Set(nodeID, 5)

		c.pushToReady(node, target)

		item := heap.Pop(&c.ready).(*traversalItem)
		assert.Equal(t, float64(5), item.yieldFactor)
	})
}

func TestCrawler_seedQueue(t *testing.T) {
	t.Run("seeds queue with closest nodes", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := mocks.NewMockDHT(ctrl)

		cfg := testCrawlerCfg()
		cfg.TraversalWidth = 3
		c := NewCrawler(0, m, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.ready)

		target := makeTestNodeID(0x80)
		var nodes []*table.Node
		for i := 0; i < 5; i++ {
			nodeID := makeTestNodeID(byte(0x10 + i))
			nodes = append(nodes, makeTestNodeWithID(nodeID, 2000+i))
		}

		m.EXPECT().Closest(target, 3).Return(nodes[:3])

		c.seedQueue(target)

		assert.Equal(t, 3, c.ready.Len())
	})

	t.Run("skips already seen nodes", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := mocks.NewMockDHT(ctrl)

		cfg := testCrawlerCfg()
		cfg.TraversalWidth = 3
		c := NewCrawler(0, m, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.ready)

		target := makeTestNodeID(0x80)
		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)

		c.seen.Set(nodeID, time.Now())

		m.EXPECT().Closest(target, 3).Return([]*table.Node{node})

		c.seedQueue(target)

		assert.Equal(t, 0, c.ready.Len())
	})
}

func TestCrawler_nextEligible(t *testing.T) {
	t.Run("returns nil when ready is empty", func(t *testing.T) {
		cfg := testCrawlerCfg()
		c := NewCrawler(0, nil, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.ready)
		heap.Init(&c.cooldown)

		assert.Nil(t, c.nextEligible(time.Now()))
	})

	t.Run("returns item from ready heap", func(t *testing.T) {
		cfg := testCrawlerCfg()
		c := NewCrawler(0, nil, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.ready)
		heap.Init(&c.cooldown)

		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)
		target := makeTestNodeID(0x20)

		item := &traversalItem{node: node, target: target, dist: target.XOR(nodeID)}
		heap.Push(&c.ready, item)

		got := c.nextEligible(time.Now())

		assert.Equal(t, item, got)
		assert.Equal(t, 0, c.ready.Len())
	})

	t.Run("promotes from cooldown before popping", func(t *testing.T) {
		cfg := testCrawlerCfg()
		c := NewCrawler(0, nil, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.ready)
		heap.Init(&c.cooldown)

		target := makeTestNodeID(0x20)
		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)
		item := &traversalItem{node: node, target: target, dist: target.XOR(nodeID)}

		past := time.Now().Add(-1 * time.Second)
		heap.Push(&c.cooldown, &cooldownItem{item: item, nextAllowed: past})

		got := c.nextEligible(time.Now())

		assert.Equal(t, item, got)
		assert.Equal(t, 0, c.ready.Len())
		assert.Equal(t, 0, c.cooldown.Len())
	})
}

func TestCrawler_processNodes(t *testing.T) {
	t.Run("inserts valid nodes and pushes to ready", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := mocks.NewMockDHT(ctrl)

		cfg := testCrawlerCfg()
		c := NewCrawler(0, m, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.ready)

		ip := net.IP{127, 0, 0, 0x11}
		validID, _ := table.DeriveNodeIDFromIP(ip)
		node := &table.Node{
			ID:   validID,
			Addr: &net.UDPAddr{IP: ip, Port: 2000},
		}
		target := makeTestNodeID(0x20)

		m.EXPECT().InsertNode(gomock.Any(), node)
		m.EXPECT().NodeCount().Return(1)

		encoded := table.EncodeNodes([]*table.Node{node})
		c.processNodes(context.Background(), encoded, target)

		assert.Equal(t, 1, c.ready.Len())
	})

	t.Run("rejects nodes with invalid ID for IP", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := mocks.NewMockDHT(ctrl)

		cfg := testCrawlerCfg()
		c := NewCrawler(0, m, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.ready)

		target := makeTestNodeID(0x20)

		nodeID := table.NodeID{}
		nodeID[0] = 0xFF
		ip := net.IP{127, 0, 0, 1}
		node := &table.Node{
			ID:   nodeID,
			Addr: &net.UDPAddr{IP: ip, Port: 2000},
		}

		m.EXPECT().NodeCount().Return(0)

		encoded := table.EncodeNodes([]*table.Node{node})
		c.processNodes(context.Background(), encoded, target)

		assert.Equal(t, 0, c.ready.Len())
	})

	t.Run("handles empty encoded string", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := mocks.NewMockDHT(ctrl)

		cfg := testCrawlerCfg()
		c := NewCrawler(0, m, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.ready)

		target := makeTestNodeID(0x20)

		m.EXPECT().NodeCount().Return(0)

		c.processNodes(context.Background(), "", target)

		assert.Equal(t, 0, c.ready.Len())
	})

	t.Run("handles malformed encoded string", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		m := mocks.NewMockDHT(ctrl)

		cfg := testCrawlerCfg()
		c := NewCrawler(0, m, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.ready)

		target := makeTestNodeID(0x20)

		c.processNodes(context.Background(), "not-valid-data", target)

		assert.Equal(t, 0, c.ready.Len())
	})
}

func TestCrawler_trimReadyToK(t *testing.T) {
	t.Run("does nothing when under capacity", func(t *testing.T) {
		cfg := testCrawlerCfg()
		cfg.TraversalWidth = 5
		c := NewCrawler(0, nil, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.ready)

		target := makeTestNodeID(0x80)
		for i := 0; i < 3; i++ {
			nodeID := makeTestNodeID(byte(0x10 + i))
			node := makeTestNodeWithID(nodeID, 2000+i)
			c.pushToReady(node, target)
		}

		c.trimReadyToK()

		assert.Equal(t, 3, c.ready.Len())
	})

	t.Run("trims to traversalWidth", func(t *testing.T) {
		cfg := testCrawlerCfg()
		cfg.TraversalWidth = 3
		c := NewCrawler(0, nil, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.ready)

		target := makeTestNodeID(0x80)
		for i := 0; i < 10; i++ {
			nodeID := makeTestNodeID(byte(0x10 + i))
			node := makeTestNodeWithID(nodeID, 2000+i)
			c.pushToReady(node, target)
		}

		c.trimReadyToK()

		assert.Equal(t, 3, c.ready.Len())
	})
}

func TestCrawler_pruneStaleInFlight(t *testing.T) {
	t.Run("removes stale entries older than 2x transaction timeout", func(t *testing.T) {
		cfg := testCrawlerCfg()
		cfg.TransactionTimeout = 100 * time.Millisecond
		c := NewCrawler(0, nil, nil, nil, cfg, recorder.NewNoOp())

		now := time.Now()
		c.now = func() time.Time { return now }

		staleID := makeTestNodeID(0x10)
		freshID := makeTestNodeID(0x11)

		c.seen.Set(staleID, now.Add(-1*time.Second))
		c.seen.Set(freshID, now)

		c.pruneStaleInFlight()

		_, staleExists := c.seen.Get(staleID)
		_, freshExists := c.seen.Get(freshID)

		assert.False(t, staleExists)
		assert.True(t, freshExists)
	})

	t.Run("does not prune nodes in cooldown", func(t *testing.T) {
		cfg := testCrawlerCfg()
		cfg.TransactionTimeout = 100 * time.Millisecond
		c := NewCrawler(0, nil, nil, nil, cfg, recorder.NewNoOp())

		heap.Init(&c.cooldown)

		now := time.Now()
		c.now = func() time.Time { return now }

		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)
		target := makeTestNodeID(0x20)

		item := &traversalItem{node: node, target: target, dist: target.XOR(nodeID)}
		c.seen.Set(nodeID, now.Add(-1*time.Second))

		future := now.Add(1 * time.Second)
		heap.Push(&c.cooldown, &cooldownItem{item: item, nextAllowed: future})

		c.pruneStaleInFlight()

		_, exists := c.seen.Get(nodeID)
		assert.True(t, exists)
	})

	t.Run("prunes oldest when over 4x traversalWidth cap", func(t *testing.T) {
		cfg := testCrawlerCfg()
		cfg.TraversalWidth = 2
		c := NewCrawler(0, nil, nil, nil, cfg, recorder.NewNoOp())

		now := time.Now()
		c.now = func() time.Time { return now }

		maxSize := 4 * cfg.TraversalWidth
		for i := 0; i < maxSize+5; i++ {
			nodeID := makeTestNodeID(byte(i))
			c.seen.Set(nodeID, now.Add(-time.Duration(maxSize+5-i)*time.Millisecond))
		}

		c.pruneStaleInFlight()

		assert.LessOrEqual(t, c.seen.Size(), maxSize)
	})
}

func TestCrawler_processSamples(t *testing.T) {
	t.Run("enqueues new samples to queue", func(t *testing.T) {
		cfg := testCrawlerCfg()
		queue := make(chan DiscoveryWork, 10)
		dedup := filter.NewBloomFilter(1_000_000, 0.001, 10*time.Minute)
		c := NewCrawler(0, nil, queue, dedup, cfg, recorder.NewNoOp())

		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)
		target := makeTestNodeID(0x20)
		item := &traversalItem{node: node, target: target}

		var sampleHash [20]byte
		sampleHash[0] = 0xAB
		samples := string(sampleHash[:])

		c.processSamples(context.Background(), samples, item)

		select {
		case work := <-queue:
			assert.Equal(t, sampleHash, work.Infohash)
		default:
			t.Fatal("expected work item in queue")
		}
	})

	t.Run("deduplicates samples", func(t *testing.T) {
		cfg := testCrawlerCfg()
		queue := make(chan DiscoveryWork, 10)
		dedup := filter.NewBloomFilter(1_000_000, 0.001, 10*time.Minute)
		c := NewCrawler(0, nil, queue, dedup, cfg, recorder.NewNoOp())

		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)
		target := makeTestNodeID(0x20)
		item := &traversalItem{node: node, target: target}

		var sampleHash [20]byte
		sampleHash[0] = 0xAB
		samples := string(sampleHash[:]) + string(sampleHash[:])

		c.processSamples(context.Background(), samples, item)

		count := 0
		for len(queue) > 0 {
			<-queue
			count++
		}
		assert.Equal(t, 1, count)
	})

	t.Run("updates nodeSamples counter", func(t *testing.T) {
		cfg := testCrawlerCfg()
		queue := make(chan DiscoveryWork, 10)
		dedup := filter.NewBloomFilter(1_000_000, 0.001, 10*time.Minute)
		c := NewCrawler(0, nil, queue, dedup, cfg, recorder.NewNoOp())

		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)
		target := makeTestNodeID(0x20)
		item := &traversalItem{node: node, target: target}

		var sampleHash [20]byte
		sampleHash[0] = 0xAB
		samples := string(sampleHash[:])

		c.processSamples(context.Background(), samples, item)

		count, ok := c.nodeSamples.Get(nodeID)
		assert.True(t, ok)
		assert.Equal(t, 1, count)
		assert.Equal(t, float64(1), item.yieldFactor)
	})

	t.Run("handles empty samples", func(t *testing.T) {
		cfg := testCrawlerCfg()
		queue := make(chan DiscoveryWork, 10)
		dedup := filter.NewBloomFilter(1_000_000, 0.001, 10*time.Minute)
		c := NewCrawler(0, nil, queue, dedup, cfg, recorder.NewNoOp())

		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)
		target := makeTestNodeID(0x20)
		item := &traversalItem{node: node, target: target}

		c.processSamples(context.Background(), "", item)

		assert.Equal(t, 0, len(queue))
	})

	t.Run("drops samples when queue is full after threshold", func(t *testing.T) {
		cfg := testCrawlerCfg()
		cfg.SampleEnqueueTimeout = 1 * time.Millisecond
		queue := make(chan DiscoveryWork, 1)
		dedup := filter.NewBloomFilter(1_000_000, 0.001, 10*time.Minute)
		c := NewCrawler(0, nil, queue, dedup, cfg, recorder.NewNoOp())

		nodeID := makeTestNodeID(0x10)
		node := makeTestNodeWithID(nodeID, 2000)
		target := makeTestNodeID(0x20)
		item := &traversalItem{node: node, target: target}

		queue <- DiscoveryWork{}

		var samples []byte
		for i := 0; i < 150; i++ {
			var h [20]byte
			h[0] = byte(i)
			h[1] = 0xCD
			samples = append(samples, h[:]...)
		}

		c.processSamples(context.Background(), string(samples), item)
	})
}
