package dht

//go:generate go run go.uber.org/mock/mockgen -destination=../mocks/mock_crawler.go -package=mocks github.com/kdwils/mgnx/dht Crawler

import (
	"bytes"
	"container/heap"
	"context"
	"crypto/rand"
	"net"
	"sync"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/logger"
)

// HarvestEvent is the only coupling point between the DHT layer (Section 3)
// and the metadata fetch layer (Section 4).
type HarvestEvent struct {
	Infohash [20]byte
	SourceIP net.IP
	Port     int
	SeenAt   time.Time
}

// Crawler is the public interface for the DHT crawler.
type Crawler interface {
	Infohashes() <-chan HarvestEvent
	Start(ctx context.Context) error
	Stop()
}

// crawler wraps Server and drives two harvest modes:
//  1. Passive: announce_peer queries already wired in the server (Step 6).
//  2. Active:  BEP-51 sample_infohashes traversal driven here.
type crawler struct {
	server  *Server
	harvest chan HarvestEvent
	dedup   *BloomFilter
	cfg     config.DHT
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewCrawler creates a Crawler backed by a new UDP Server. The BloomFilter is
// created here and wired into the server so that both the passive
// announce_peer path and the active BEP-51 path share the same dedup state.
func NewCrawler(cfg config.DHT) (Crawler, error) {
	server, err := NewServer(cfg)
	if err != nil {
		return nil, err
	}
	dedup := NewBloomFilter()
	server.dedup = dedup
	return &crawler{
		server:  server,
		harvest: server.harvest,
		dedup:   dedup,
		cfg:     cfg,
	}, nil
}

// Infohashes returns the channel of harvested infohash events.
func (c *crawler) Infohashes() <-chan HarvestEvent {
	return c.harvest
}

// Start launches the server, runs bootstrap convergence, and starts the
// BEP-51 active traversal workers.
func (c *crawler) Start(ctx context.Context) error {
	if err := c.server.Start(ctx); err != nil {
		return err
	}

	if err := c.server.Bootstrap(ctx, c.cfg.BootstrapNodes); err != nil {
		return err
	}

	crawlCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	workers := c.cfg.BEP51Workers
	if workers <= 0 {
		workers = 2
	}
	for range workers {
		c.wg.Go(func() {
			c.bep51Worker(crawlCtx)
		})
	}

	logger.FromContext(ctx).Info("DHT crawler started", "bep51_workers", workers)
	return nil
}

// Stop cancels the BEP-51 workers, waits for them to exit, then shuts down
// the server (closing the socket and persisting the routing table).
func (c *crawler) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	c.server.Stop()
}

// bep51Item is an element of the BEP-51 traversal priority queue.
type bep51Item struct {
	node   *Node
	target NodeID
	dist   NodeID // XOR(node.ID, target); used for min-heap ordering
	index  int
}

// bep51PQ is a min-heap of *bep51Item ordered by XOR distance to target.
type bep51PQ []*bep51Item

func (pq bep51PQ) Len() int { return len(pq) }
func (pq bep51PQ) Less(i, j int) bool {
	return bytes.Compare(pq[i].dist[:], pq[j].dist[:]) < 0
}
func (pq bep51PQ) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}
func (pq *bep51PQ) Push(x any) {
	item := x.(*bep51Item)
	item.index = len(*pq)
	*pq = append(*pq, item)
}
func (pq *bep51PQ) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	*pq = old[:n-1]
	item.index = -1
	return item
}

// retargetEvery is the number of queries each worker issues before
// randomising its target to spread coverage across the full keyspace.
const retargetEvery = 200

// bep51Worker implements the BEP-51 active traversal loop. It maintains its
// own priority queue (ordered by XOR distance to the current target) and a
// per-node cooldown map so it respects the Interval returned by each node.
// When the queue is exhausted the worker picks a new random target and
// re-seeds from the routing table.
func (c *crawler) bep51Worker(ctx context.Context) {
	var target NodeID
	rand.Read(target[:])

	pq := &bep51PQ{}
	heap.Init(pq)

	seen := make(map[NodeID]time.Time) // node ID → earliest time we may re-query

	c.seedQueue(pq, target)

	queried := 0
	for {
		if ctx.Err() != nil {
			return
		}

		if queried > 0 && queried%retargetEvery == 0 {
			rand.Read(target[:])
			c.seedQueue(pq, target)
		}

		item := c.nextEligible(pq, seen)
		if item == nil {
			rand.Read(target[:])
			c.seedQueue(pq, target)
			if pq.Len() == 0 {
				select {
				case <-time.After(5 * time.Second):
				case <-ctx.Done():
					return
				}
			}
			continue
		}

		qCtx, cancel := context.WithTimeout(ctx, c.cfg.TransactionTimeout)
		resp, err := c.server.Query(qCtx, item.node.Addr, item.node.ID, &Msg{
			Y: "q",
			Q: "sample_infohashes",
			A: &MsgArgs{
				ID:     string(c.server.ourID[:]),
				Target: string(item.target[:]),
			},
		})
		cancel()
		queried++

		interval := 10 * time.Second // default re-query interval

		if err == nil && resp != nil && resp.R != nil {
			if resp.R.Interval > 0 {
				d := time.Duration(resp.R.Interval) * time.Second
				if d > interval {
					interval = d
				}
			}
			c.processSamples(ctx, resp.R.Samples, item)
			c.processNodes(resp.R.Nodes, item.target, pq)
		}

		seen[item.node.ID] = time.Now().Add(interval)
	}
}

// seedQueue pushes the k closest routing-table nodes to target onto pq.
func (c *crawler) seedQueue(pq *bep51PQ, target NodeID) {
	k := c.cfg.BucketSize
	if k <= 0 {
		k = 8
	}
	for _, n := range c.server.table.Closest(target, k) {
		heap.Push(pq, &bep51Item{
			node:   n,
			target: target,
			dist:   target.XOR(n.ID),
		})
	}
}

// nextEligible pops items from pq until it finds one not in the cooldown
// window. Returns nil if the queue is empty.
func (c *crawler) nextEligible(pq *bep51PQ, seen map[NodeID]time.Time) *bep51Item {
	for pq.Len() > 0 {
		item := heap.Pop(pq).(*bep51Item)
		if t, ok := seen[item.node.ID]; !ok || time.Now().After(t) {
			return item
		}
	}
	return nil
}

// processSamples decodes the raw 20-byte infohash samples from a BEP-51
// response. For each new infohash, it launches a goroutine that does a
// get_peers query to the source node to find actual swarm peers, then emits
// HarvestEvents with those peer addresses. Using the DHT node's address
// directly would fail — DHT nodes are UDP-only and don't serve BEP-09 metadata.
func (c *crawler) processSamples(ctx context.Context, samples string, item *bep51Item) {
	total := len(samples) / 20
	if total == 0 {
		return
	}
	new := 0
	for i := 0; i+20 <= len(samples); i += 20 {
		var h [20]byte
		copy(h[:], samples[i:i+20])
		if c.dedup.SeenOrAdd(h) {
			continue
		}
		new++
		go c.findAndHarvest(ctx, h, item.node)
	}
	logger.FromContext(ctx).Debug("samples processed",
		"node", item.node.Addr,
		"total", total,
		"new", new,
	)
}

// findAndHarvest performs a get_peers query for infohash against node. If the
// node returns peer values (compact 6-byte peer records), each peer is emitted
// as a HarvestEvent. If the node only returns closer nodes (no values), the
// infohash is silently dropped — a full iterative get_peers lookup would be
// needed to find peers, which is too expensive to do inline.
func (c *crawler) findAndHarvest(ctx context.Context, h [20]byte, node *Node) {
	qCtx, cancel := context.WithTimeout(ctx, c.cfg.TransactionTimeout)
	defer cancel()

	resp, err := c.server.Query(qCtx, node.Addr, node.ID, &Msg{
		Y: "q",
		Q: "get_peers",
		A: &MsgArgs{
			ID:       string(c.server.ourID[:]),
			InfoHash: string(h[:]),
		},
	})
	if err != nil {
		return
	}
	if resp.R == nil {
		return
	}
	if len(resp.R.Values) == 0 {
		return
	}

	peers, err := DecodePeers(resp.R.Values)
	if err != nil {
		return
	}

	for _, peer := range peers {
		udpAddr, ok := peer.(*net.UDPAddr)
		if !ok {
			continue
		}
		event := HarvestEvent{
			Infohash: h,
			SourceIP: udpAddr.IP,
			Port:     udpAddr.Port,
			SeenAt:   time.Now(),
		}
		select {
		case c.harvest <- event:
		default: // channel full — drop
		}
	}
}

// processNodes decodes compact node records from a BEP-51 response, inserts
// them into the routing table, and enqueues them for future traversal.
func (c *crawler) processNodes(encoded string, target NodeID, pq *bep51PQ) {
	nodes, err := DecodeNodes(encoded)
	if err != nil {
		return
	}
	for _, n := range nodes {
		c.server.table.Insert(n)
		heap.Push(pq, &bep51Item{
			node:   n,
			target: target,
			dist:   target.XOR(n.ID),
		})
	}
}
