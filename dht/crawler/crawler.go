package crawler

import (
	"container/heap"
	"context"
	"crypto/rand"
	"encoding/hex"
	mrand "math/rand"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/dht/filter"
	"github.com/kdwils/mgnx/dht/krpc"
	"github.com/kdwils/mgnx/dht/table"
	"github.com/kdwils/mgnx/dht/types"
	"github.com/kdwils/mgnx/logger"
	pkgcache "github.com/kdwils/mgnx/pkg/cache"
	"github.com/kdwils/mgnx/recorder"
)

//go:generate go run go.uber.org/mock/mockgen -destination=mocks/mock_dht.go -package=mocks github.com/kdwils/mgnx/dht/crawler DHT

// DHT is the interface a Crawler or DiscoveryWorker uses to interact with the
// DHT server. *Server satisfies this interface.
type DHT interface {
	GetPeers(ctx context.Context, addr *net.UDPAddr, remoteID, infoHash table.NodeID) (*krpc.Msg, error)
	SampleInfohashes(ctx context.Context, addr *net.UDPAddr, remoteID, target table.NodeID) (*krpc.Msg, error)
	FindNode(ctx context.Context, addr *net.UDPAddr, remoteID, target table.NodeID) (*krpc.Msg, error)
	Closest(target table.NodeID, n int) []*table.Node
	NodeCount() int
	MarkSuccess(id table.NodeID)
	MarkFailure(id table.NodeID)
	Emit(event types.DiscoveredPeers)
	InsertNode(ctx context.Context, node *table.Node)
}

// Crawler is a single BEP-51 active traversal worker. Multiple instances run
// concurrently; each is independent and maintains its own heap and seen map.
type Crawler struct {
	id                 int
	dht                DHT
	queue              chan<- DiscoveryWork
	dedup              *filter.BloomFilter
	rec                *recorder.Recorder
	cfg                config.Crawler
	seen               map[table.NodeID]time.Time
	ready              traversalHeap
	cooldown           cooldownHeap
	nodeSampleSupport  *pkgcache.Cache[table.NodeID, bool]
	droppedDiscoveries atomic.Int64
}

// NewCrawler constructs a Crawler. queue is the write side of the shared
// DiscoveryWork channel. dedup is obtained from server.Dedup().
func NewCrawler(id int, dht DHT, queue chan<- DiscoveryWork, dedup *filter.BloomFilter, cfg config.Crawler, rec *recorder.Recorder) *Crawler {
	return &Crawler{
		id:       id,
		dht:      dht,
		queue:    queue,
		dedup:    dedup,
		rec:      rec,
		cfg:      cfg,
		seen:     make(map[table.NodeID]time.Time),
		ready:    make(traversalHeap, 0),
		cooldown: make(cooldownHeap, 0),
		nodeSampleSupport: pkgcache.New(
			pkgcache.WithCleanup(
				cfg.NodeCacheCleanup,
				func(_ table.NodeID, _ bool) bool { return true },
			),
		),
	}
}

// Start runs the BEP-51 traversal loop, blocking until ctx is cancelled.
func (c *Crawler) Start(ctx context.Context) {
	c.nodeSampleSupport.StartCleanup(ctx)
	heap.Init(&c.ready)
	heap.Init(&c.cooldown)
	c.crawl(ctx)
}

// DiscoveryWorker consumes DiscoveryWork items from the queue and performs
// iterative BEP-05 get_peers lookups to find actual swarm peers.
type DiscoveryWorker struct {
	id    int
	dht   DHT
	queue <-chan DiscoveryWork
	rec   *recorder.Recorder
	cfg   config.Crawler
}

// traversalItem is an element of the traversal heap.
type traversalItem struct {
	node     *table.Node
	target   table.NodeID
	dist     table.NodeID
	index    int
	failures int
}

// queryForSamples tries sample_infohashes (BEP-51) against the node first.
// If the node responds with a 204 "method unknown" error, that fact is cached
// and nil is returned. Subsequent calls for the same node skip the query.
func (c *Crawler) queryForSamples(ctx context.Context, item *traversalItem) (resp *krpc.Msg, supported bool, err error) {
	log := logger.FromContext(ctx).With("service", "crawler", "node", item.node.Addr.String(), "target", hex.EncodeToString(item.target[:]))

	capable, known := c.nodeSampleSupport.Get(item.node.ID)
	if known && !capable {
		return nil, false, nil
	}

	log.Debug("sending query", "type", "sample_infohashes")
	c.rec.IncCrawlerQueriesTotal("sample_infohashes", "crawling", c.id)
	resp, err = c.dht.SampleInfohashes(ctx, item.node.Addr, item.node.ID, item.target)
	if err != nil {
		return resp, false, err
	}
	if !krpc.IsMethodUnknown(resp) {
		c.nodeSampleSupport.Set(item.node.ID, true)
		return resp, true, nil
	}

	c.nodeSampleSupport.Set(item.node.ID, false)
	return nil, false, nil
}

// jitter returns a random duration in [0, max) to spread cooldown expiries.
func jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(mrand.Int63n(int64(max)))
}

// computeInterval returns the re-query interval for a node based on its BEP-51 response.
func (c *Crawler) computeInterval(resp *krpc.Msg) time.Duration {
	if resp == nil || resp.R == nil || resp.R.Interval <= 0 {
		return c.cfg.DefaultInterval
	}
	d := time.Duration(resp.R.Interval) * time.Second
	if d < c.cfg.DefaultInterval {
		return c.cfg.DefaultInterval
	}
	if c.cfg.MaxInterval > 0 && d > c.cfg.MaxInterval {
		return c.cfg.MaxInterval
	}
	return d
}

// promoteReady moves nodes from the cooldown heap to the ready heap when their
// cooldown has expired.
func (c *Crawler) promoteReady(now time.Time) {
	for c.cooldown.Len() > 0 {
		top := c.cooldown[0]
		if top.nextAllowed.After(now) {
			break
		}
		heap.Pop(&c.cooldown)
		heap.Push(&c.ready, top.item)
	}
}

// crawl implements the BEP-51 active traversal loop.
func (c *Crawler) crawl(ctx context.Context) {
	log := logger.FromContext(ctx).With("service", "crawler")

	var target table.NodeID
	rand.Read(target[:])

	c.seedQueue(target)

	type queryResult struct {
		item *traversalItem
		resp *krpc.Msg
		err  error
	}

	var iteration int
	for {
		if ctx.Err() != nil {
			return
		}

		c.rec.SetCrawlerTraversalQueueSize(c.id, float64(c.ready.Len()))
		c.rec.SetCrawlerCooldownsActive(c.id, float64(c.cooldown.Len()))

		if c.ready.Len() < c.cfg.Alpha {
			rand.Read(target[:])
			c.seedQueue(target)

			if dropped := c.droppedDiscoveries.Swap(0); dropped > 0 {
				log.Warn("discovery queue backpressure",
					"dropped", dropped,
					"queue_len", len(c.queue),
					"queue_cap", cap(c.queue),
				)
			}
			log.Debug("retargeting",
				"ready_size", c.ready.Len(),
				"cooldown_size", c.cooldown.Len(),
				"routing_table_nodes", c.dht.NodeCount(),
				"target", hex.EncodeToString(target[:]),
			)
		}

		now := time.Now()
		var batch []*traversalItem
		for len(batch) < c.cfg.Alpha {
			item := c.nextEligible(now)
			if item == nil {
				break
			}
			c.seen[item.node.ID] = now
			batch = append(batch, item)
		}

		if len(batch) == 0 {
			if c.cooldown.Len() > 0 {
				wait := time.Until(c.cooldown[0].nextAllowed)
				if wait > 0 {
					select {
					case <-time.After(wait):
					case <-ctx.Done():
						return
					}
				}
				continue
			}

			rand.Read(target[:])
			c.seedQueue(target)

			if c.ready.Len() == 0 {
				select {
				case <-time.After(c.cfg.EmptySpinWait):
				case <-ctx.Done():
					return
				}
			}
			continue
		}

		results := make([]queryResult, len(batch))
		var wg sync.WaitGroup
		for i, item := range batch {
			wg.Add(1)
			i, item := i, item
			go func() {
				defer wg.Done()

				resp, err := c.dht.GetPeers(ctx, item.node.Addr, item.node.ID, item.target)
				c.rec.IncCrawlerQueriesTotal("get_peers", "crawling", c.id)

				sResp, supported, sErr := c.queryForSamples(ctx, item)
				if sErr == nil && supported && sResp != nil && sResp.R != nil && len(sResp.R.Samples) > 0 {
					c.processSamples(ctx, sResp.R.Samples, item)
				}

				results[i] = queryResult{item, resp, err}
			}()
		}
		wg.Wait()

		for _, r := range results {
			if r.err != nil {
				c.dht.MarkFailure(r.item.node.ID)
				r.item.failures++
				if r.item.failures >= c.cfg.MaxNodeFailures {
					delete(c.seen, r.item.node.ID)
					continue
				}
				next := time.Now().Add(c.cfg.DefaultCooldown + jitter(c.cfg.MaxJitter))
				c.seen[r.item.node.ID] = next
				heap.Push(&c.cooldown, &cooldownItem{item: r.item, nextAllowed: next})
				continue
			}

			if r.resp != nil && r.resp.R != nil {
				if len(r.resp.R.Nodes) > 0 {
					c.processNodes(ctx, r.resp.R.Nodes, r.item.target)
				}
			}

			c.dht.MarkSuccess(r.item.node.ID)
			r.item.failures = 0
			next := time.Now().Add(c.computeInterval(r.resp) + jitter(c.cfg.MaxJitter))
			c.seen[r.item.node.ID] = next
			heap.Push(&c.cooldown, &cooldownItem{item: r.item, nextAllowed: next})

			nodesReturned := 0
			if r.resp != nil && r.resp.R != nil {
				nodesReturned = len(r.resp.R.Nodes) / 26
			}
			log.Debug("query complete",
				"node", r.item.node.Addr.String(),
				"ready_size", c.ready.Len(),
				"cooldown_size", c.cooldown.Len(),
				"nodes_returned", nodesReturned,
				"table_size", c.dht.NodeCount(),
			)
		}

		iteration++
		if iteration%50 == 0 {
			c.pruneStaleInFlight()
		}
		if iteration%c.cfg.MaxIterations == 0 {
			rand.Read(target[:])
			c.ready = traversalHeap{}
			heap.Init(&c.ready)
			c.seedQueue(target)
			log.Debug("forced target rotation",
				"iteration", iteration,
				"target", hex.EncodeToString(target[:]),
				"routing_table_nodes", c.dht.NodeCount(),
			)
		}
	}
}

// pruneStaleInFlight removes entries from the seen map whose timestamps are
// older than 2× the transaction timeout and enforces a 4× traversalWidth cap.
func (c *Crawler) pruneStaleInFlight() {
	cutoff := time.Now().Add(-2 * c.cfg.TransactionTimeout)
	for id, t := range c.seen {
		if t.Before(cutoff) {
			delete(c.seen, id)
		}
	}

	maxSize := 4 * c.cfg.TraversalWidth
	if len(c.seen) <= maxSize {
		return
	}
	now := time.Now()
	for id, t := range c.seen {
		if len(c.seen) <= maxSize {
			break
		}
		if !t.After(now) {
			delete(c.seen, id)
		}
	}
}

// seedQueue pushes the k closest routing-table nodes to the ready heap.
func (c *Crawler) seedQueue(target table.NodeID) {
	for _, n := range c.dht.Closest(target, c.cfg.TraversalWidth) {
		if _, inCooldown := c.seen[n.ID]; inCooldown {
			continue
		}
		heap.Push(&c.ready, &traversalItem{
			node:   n,
			target: target,
			dist:   target.XOR(n.ID),
		})
	}
}

// nextEligible promotes cooled-down nodes then pops the closest ready node.
func (c *Crawler) nextEligible(now time.Time) *traversalItem {
	c.promoteReady(now)

	if c.ready.Len() == 0 {
		return nil
	}

	return heap.Pop(&c.ready).(*traversalItem)
}

// processSamples decodes the raw 20-byte infohash samples from a BEP-51
// response and enqueues new ones for discovery.
func (c *Crawler) processSamples(ctx context.Context, samples string, item *traversalItem) {
	total := len(samples) / 20
	if total == 0 {
		return
	}
	new := 0
	dropped := 0
	for i := 0; i+20 <= len(samples); i += 20 {
		var h [20]byte
		copy(h[:], samples[i:i+20])
		if c.dedup.SeenOrAdd(h) {
			continue
		}
		new++
		select {
		case c.queue <- DiscoveryWork{Infohash: h, Source: item.node}:
		case <-time.After(c.cfg.SampleEnqueueTimeout):
			dropped++
			c.droppedDiscoveries.Add(1)
		}
	}
	if c.rec != nil {
		c.rec.SetDHTQueueCapacity(float64(cap(c.queue)))
		duplicate := total - new - dropped
		c.rec.AddCrawlerSamplesTotal("new", new)
		c.rec.AddCrawlerSamplesTotal("duplicate", duplicate)
		if dropped > 0 {
			c.rec.IncDHTDiscoveryQueueDroppedTotal()
			c.rec.AddCrawlerSamplesTotal("dropped", dropped)
		}
	}
	logger.FromContext(ctx).Debug("samples processed",
		"service", "crawler",
		"node", item.node.Addr.String(),
		"total", total,
		"new", new,
	)
}

// trimReadyToK sorts the ready heap by XOR distance and discards all items
// beyond traversalWidth.
func (c *Crawler) trimReadyToK() {
	if c.ready.Len() <= c.cfg.TraversalWidth {
		return
	}
	sort.Sort(&c.ready)
	c.ready = c.ready[:c.cfg.TraversalWidth]
	heap.Init(&c.ready)
}

// processNodes decodes compact node records from a BEP-51 sample_infohashes
// response, inserts them into the routing table, and enqueues eligible nodes.
func (c *Crawler) processNodes(ctx context.Context, encoded string, target table.NodeID) {
	nodes, err := table.DecodeNodes(encoded)
	if err != nil {
		return
	}
	inserted, queued := 0, 0
	for _, n := range nodes {
		c.dht.InsertNode(ctx, n)
		inserted++
		if _, inCooldown := c.seen[n.ID]; inCooldown {
			continue
		}
		heap.Push(&c.ready, &traversalItem{
			node:   n,
			target: target,
			dist:   target.XOR(n.ID),
		})
		queued++
	}
	c.trimReadyToK()
	logger.FromContext(ctx).Debug("process nodes",
		"service", "crawler",
		"total", len(nodes),
		"inserted", inserted,
		"queued", queued,
		"table_size", c.dht.NodeCount(),
	)
}
