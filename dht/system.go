package dht

import (
	"context"
	"net"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/dht/crawler"
	"github.com/kdwils/mgnx/dht/filter"
	"github.com/kdwils/mgnx/dht/server"
	"github.com/kdwils/mgnx/dht/types"
	"github.com/kdwils/mgnx/recorder"
)

// System coordinates the DHT server, crawlers, and discovery workers.
type System struct {
	server           *server.Server
	crawlers         []*crawler.Crawler
	discoveryWorkers []*crawler.DiscoveryWorker
	discovered       chan types.DiscoveredPeers
	dedup            *filter.BloomFilter
}

// NewSystem wires up the DHT subsystems.
func NewSystem(cfg config.Config, ip net.IP, conn server.Conn, rec *recorder.Recorder) (*System, error) {
	discovered := make(chan types.DiscoveredPeers, cfg.DHT.DiscoveryBuffer)
	dedup := filter.NewBloomFilter(cfg.DHT.BloomN, cfg.DHT.BloomP, cfg.DHT.BloomRotation)

	srvr, err := server.NewServer(cfg.DHT, ip, conn, rec, discovered, dedup)
	if err != nil {
		return nil, err
	}

	queue := make(chan crawler.DiscoveryWork, cfg.Crawler.DiscoveryQueueSize)

	crawlers := make([]*crawler.Crawler, cfg.Crawler.Crawlers)
	for i := 0; i < cfg.Crawler.Crawlers; i++ {
		crawlers[i] = crawler.NewCrawler(i, srvr, queue, dedup, cfg.Crawler, rec)
	}

	discoveryWorkers := make([]*crawler.DiscoveryWorker, cfg.Crawler.DiscoveryWorkers)
	for i := 0; i < cfg.Crawler.DiscoveryWorkers; i++ {
		discoveryWorkers[i] = crawler.NewDiscoveryWorker(i, srvr, queue, cfg.Crawler, rec)
	}

	return &System{
		server:           srvr,
		crawlers:         crawlers,
		discoveryWorkers: discoveryWorkers,
		discovered:       discovered,
		dedup:            dedup,
	}, nil
}

// Start launches the server and all workers. It blocks until the server has
// finished its initial bootstrap.
func (s *System) Start(ctx context.Context) error {
	if err := s.server.Start(ctx); err != nil {
		return err
	}

	go s.dedup.Rotate(ctx)

	for _, c := range s.crawlers {
		go c.Start(ctx)
	}
	for _, w := range s.discoveryWorkers {
		go w.Start(ctx)
	}

	return nil
}

// Stop shuts down the DHT system.
func (s *System) Stop(ctx context.Context) error {
	return s.server.Stop(ctx)
}

// Discovered returns the channel on which discovered peers are emitted.
func (s *System) Discovered() <-chan types.DiscoveredPeers {
	return s.discovered
}

// NodeCount returns the current number of nodes in the routing table.
func (s *System) NodeCount() int {
	return s.server.NodeCount()
}
