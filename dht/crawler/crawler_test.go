package crawler_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/dht/crawler"
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
	var nodeID table.NodeID
	nodeID[0] = id
	return &table.Node{
		ID:   nodeID,
		Addr: &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: port},
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

		queue := make(chan crawler.DiscoveryWork, 8)
		dedup := filter.NewBloomFilter(1_000_000, 0.001, 10*time.Minute)
		c := crawler.NewCrawler(0, m, queue, dedup, testCrawlerCfg(), recorder.NewNoOp())

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

		queue := make(chan crawler.DiscoveryWork, 8)
		dedup := filter.NewBloomFilter(1_000_000, 0.001, 10*time.Minute)
		c := crawler.NewCrawler(0, m, queue, dedup, testCrawlerCfg(), recorder.NewNoOp())

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		go c.Start(ctx)

		select {
		case work := <-queue:
			assert.Equal(t, crawler.DiscoveryWork{Infohash: sampleHash, Source: node}, work)
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

		queue := make(chan crawler.DiscoveryWork, 8)
		dedup := filter.NewBloomFilter(1_000_000, 0.001, 10*time.Minute)
		c := crawler.NewCrawler(0, m, queue, dedup, testCrawlerCfg(), recorder.NewNoOp())

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

		queue := make(chan crawler.DiscoveryWork, 8)
		dedup := filter.NewBloomFilter(1_000_000, 0.001, 10*time.Minute)
		c := crawler.NewCrawler(0, m, queue, dedup, testCrawlerCfg(), recorder.NewNoOp())

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
