package dht

import (
	"bytes"
	"container/heap"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/logger"
	pkgcache "github.com/kdwils/mgnx/pkg/cache"
	"github.com/kdwils/mgnx/recorder"
)

// DiscoveredPeers is the only coupling point between the DHT layer (Section 3)
// and the metadata fetch layer (Section 4).
type DiscoveredPeers struct {
	Infohash [20]byte
	Peers    []PeerAddr
	SeenAt   time.Time
}

// PeerAddr represents a TCP peer address for metadata fetching.
type PeerAddr struct {
	SourceIP net.IP
	Port     int
}

// crawler wraps Server and drives two discovered modes:
//  1. Passive: announce_peer queries already wired in the server (Step 6).
//  2. Active:  BEP-51 sample_infohashes traversal driven here.
type crawler struct {
	server             *Server
	discovered         chan DiscoveredPeers
	dedup              *BloomFilter
	rec                *recorder.Recorder
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
	nodeSampleSupport  *pkgcache.Cache[NodeID, bool]
	discoveryQueue     chan discoveryWork
	droppedDiscoveries atomic.Int64
	bootstrapNodes     []string
	discoverQueueSize  int
	discoveryWorkers   int
	crawlers           int
	transactionTimeout time.Duration
	traversalWidth     int
	alpha              int
	maxIterations      int
}

type discoveryWork struct {
	infohash   [20]byte
	sourceNode *Node
}

// NewCrawler creates a Crawler backed by a new UDP Server. The BloomFilter is
// created here and wired into the server so that both the passive
// announce_peer path and the active BEP-51 path share the same dedup state.
func NewCrawler(cfg config.Crawler, dhtCfg config.DHT, rec *recorder.Recorder) (*crawler, error) {
	server, err := NewServer(dhtCfg, rec)
	if err != nil {
		return nil, err
	}
	dedup := NewBloomFilter()
	server.dedup = dedup
	return &crawler{
		server:             server,
		discovered:         server.discovered,
		dedup:              dedup,
		rec:                rec,
		bootstrapNodes:     cfg.BootstrapNodes,
		crawlers:           cfg.Crawlers,
		discoveryWorkers:   cfg.DiscoveryWorkers,
		discoverQueueSize:  cfg.DiscoveryQueueSize,
		discoveryQueue:     make(chan discoveryWork, cfg.DiscoveryQueueSize),
		traversalWidth:     cfg.TraversalWidth,
		transactionTimeout: dhtCfg.TransactionTimeout,
		alpha:              cfg.Alpha,
		maxIterations:      cfg.MaxIterations,
		nodeSampleSupport: pkgcache.New[NodeID, bool](
			pkgcache.WithCleanup[NodeID, bool](
				1*time.Hour,
				func(_ NodeID, _ bool) bool { return true },
			),
		),
	}, nil
}

// Infohashes returns the channel of discovered infohash events.
func (c *crawler) Infohashes() <-chan DiscoveredPeers {
	return c.discovered
}

// NodeCount returns the number of nodes currently in the routing table.
func (c *crawler) NodeCount() int {
	return c.server.table.NodeCount()
}

type crawlerInstance struct {
	id       int
	seen     map[NodeID]time.Time
	ready    traversalHeap
	cooldown cooldownHeap
	*crawler
}

type discoveryWorker struct {
	id int
	*crawler
}

// Start launches the server, runs bootstrap convergence, and starts the
// BEP-51 active traversal workers.
func (c *crawler) Start(ctx context.Context) error {
	if err := c.server.Start(ctx); err != nil {
		return err
	}

	if err := c.server.Bootstrap(ctx, c.bootstrapNodes); err != nil {
		return err
	}

	if c.server.table.NodeCount() == 0 {
		return fmt.Errorf("bootstrap failed: no nodes in routing table")
	}

	crawlCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	c.nodeSampleSupport.StartCleanup(crawlCtx)

	for i := 0; i < c.crawlers; i++ {
		c.wg.Go(func() {
			instance := crawlerInstance{
				id:       i,
				crawler:  c,
				ready:    make(traversalHeap, 0),
				cooldown: make(cooldownHeap, 0),
				seen:     make(map[NodeID]time.Time),
			}
			instance.crawl(crawlCtx, i)
		})
	}
	for i := 0; i < c.discoveryWorkers; i++ {
		c.wg.Go(func() {
			w := discoveryWorker{
				id:      i,
				crawler: c,
			}
			w.discover(crawlCtx)
		})
	}

	logger.FromContext(ctx).Info("crawler started",
		"service", "crawler",
		"crawlers", c.crawlers,
		"discovery_workers", c.discoveryWorkers,
		"discovery_channel_size", cap(c.discoveryQueue),
	)
	return nil
}

// Stop cancels the traversal workers, waits for them to exit, then shuts down
// the server (closing the socket and persisting the routing table).
// The caller's context is ignored — it is likely already cancelled at shutdown
// time. A fresh timeout context is used for the routing table save.
func (c *crawler) Stop(_ context.Context) error {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.server.Stop(saveCtx)
}

// traversalItem is an element of the traversal heap.
type traversalItem struct {
	node     *Node
	target   NodeID
	dist     NodeID // XOR(node.ID, target); used for min-heap ordering
	index    int
	failures int
}

// cooldownItem holds a traversalItem waiting for its cooldown to expire.
type cooldownItem struct {
	item        *traversalItem
	nextAllowed time.Time
}

// cooldownHeap is a min-heap of *cooldownItem ordered by nextAllowed time.
type cooldownHeap []*cooldownItem

func (h cooldownHeap) Len() int           { return len(h) }
func (h cooldownHeap) Less(i, j int) bool { return h[i].nextAllowed.Before(h[j].nextAllowed) }
func (h cooldownHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *cooldownHeap) Push(x any)        { *h = append(*h, x.(*cooldownItem)) }
func (h *cooldownHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// traversalHeap is a min-heap of *traversalItem ordered by XOR distance to target.
type traversalHeap []*traversalItem

func (pq traversalHeap) Len() int { return len(pq) }
func (pq traversalHeap) Less(i, j int) bool {
	return bytes.Compare(pq[i].dist[:], pq[j].dist[:]) < 0
}
func (pq traversalHeap) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}
func (pq *traversalHeap) Push(x any) {
	item := x.(*traversalItem)
	item.index = len(*pq)
	*pq = append(*pq, item)
}
func (pq *traversalHeap) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	*pq = old[:n-1]
	item.index = -1
	return item
}

// getPeers sends a BEP-05 get_peers query for item.target and returns the raw response.
func (c *crawler) getPeers(ctx context.Context, item *traversalItem) (*Msg, error) {
	return c.server.Query(ctx, item.node.Addr, item.node.ID, &Msg{
		Y: "q", Q: "get_peers",
		A: &MsgArgs{
			ID:       string(c.server.ourID[:]),
			InfoHash: string(item.target[:]),
		},
	})
}

// queryForSamples tries sample_infohashes (BEP-51) against the node first.
// If the node responds with a 204 "method unknown" error it doesn't support
// BEP-51; that fact is cached and get_peers is used as a fallback for routing
// continuation. Subsequent calls for the same node skip straight to get_peers.
func (c *crawlerInstance) queryForSamples(ctx context.Context, item *traversalItem) (*Msg, error) {
	log := logger.FromContext(ctx).With("service", "crawler", "node", item.node.Addr.String(), "target", hex.EncodeToString(item.target[:]))

	capable, known := c.nodeSampleSupport.Get(item.node.ID)
	if known && !capable {
		return nil, nil
	}

	log.Debug("sending query", "type", "sample_infohashes")
	c.rec.IncCrawlerQueriesTotal("sample_infohashes", "crawling", c.id)
	resp, err := c.server.Query(ctx, item.node.Addr, item.node.ID, &Msg{
		Y: "q", Q: "sample_infohashes",
		A: &MsgArgs{
			ID:     string(c.server.ourID[:]),
			Target: string(item.target[:]),
		},
	})
	if err != nil {
		return resp, err
	}
	if !isMethodUnknown(resp) {
		c.nodeSampleSupport.Set(item.node.ID, true)
		return resp, nil
	}

	c.nodeSampleSupport.Set(item.node.ID, false)
	return nil, nil
}

const (
	defaultCooldown = 2 * time.Second  // retry interval after a failed/timed-out query
	defaultInterval = 10 * time.Second // re-query interval when BEP-51 response carries no Interval
	maxNodeFailures = 3                // evict node after this many consecutive failures
)

// computeInterval returns the re-query interval for a node.
// BEP-51 nodes advertise an Interval field; default to defaultInterval if absent or too small.
func computeInterval(resp *Msg) time.Duration {
	if resp == nil || resp.R == nil || resp.R.Interval <= 0 {
		return defaultInterval
	}
	d := time.Duration(resp.R.Interval) * time.Second
	if d < defaultInterval {
		return defaultInterval
	}
	return d
}

// promoteReady moves nodes from the cooldown heap to the ready heap when their
// cooldown has expired. The seen entry is intentionally retained until
// nextEligible pops the item, so seedQueue/processNodes cannot double-insert.
func (c *crawlerInstance) promoteReady(now time.Time) {
	for c.cooldown.Len() > 0 {
		top := c.cooldown[0]
		if top.nextAllowed.After(now) {
			break
		}
		heap.Pop(&c.cooldown)
		heap.Push(&c.ready, top.item)
	}
}

// crawl implements the BEP-51 active traversal loop. Eligible nodes live in
// the ready heap; nodes waiting for their cooldown live in the cooldown heap.
// seen is the single authoritative dedup guard: a key present in seen means
// the node is in cooldownHeap and must not be pushed to readyHeap.
func (c *crawlerInstance) crawl(ctx context.Context, id int) {
	log := logger.FromContext(ctx).With("service", "crawler")

	var target NodeID
	rand.Read(target[:])

	heap.Init(&c.ready)
	heap.Init(&c.cooldown)

	c.seedQueue(target)

	for {
		if ctx.Err() != nil {
			return
		}

		c.rec.SetCrawlerTraversalQueueSize(id, float64(c.ready.Len()))
		c.rec.SetCrawlerCooldownsActive(id, float64(c.cooldown.Len()))

		if c.ready.Len() < c.traversalWidth {
			rand.Read(target[:])
			c.seedQueue(target)

			if dropped := c.droppedDiscoveries.Swap(0); dropped > 0 {
				log.Warn("discovery queue backpressure",
					"dropped", dropped,
					"queue_len", len(c.discoveryQueue),
					"queue_cap", cap(c.discoveryQueue),
				)
			}
			log.Debug("retargeting",
				"ready_size", c.ready.Len(),
				"cooldown_size", c.cooldown.Len(),
				"routing_table_nodes", c.server.table.NodeCount(),
				"target", hex.EncodeToString(target[:]),
			)
		}

		item := c.nextEligible(time.Now())
		if item == nil {
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

			// truly empty → reseed
			rand.Read(target[:])
			c.seedQueue(target)

			if c.ready.Len() == 0 {
				// routing table also empty; avoid tight spin
				select {
				case <-time.After(5 * time.Second):
				case <-ctx.Done():
					return
				}
			}
			continue
		}

		qCtx, cancel := context.WithTimeout(ctx, c.transactionTimeout)
		resp, err := c.queryForSamples(qCtx, item)
		cancel()

		if err != nil {
			item.failures++
			if item.failures >= maxNodeFailures {
				delete(c.seen, item.node.ID)
				continue
			}
			next := time.Now().Add(defaultCooldown)
			c.seen[item.node.ID] = next
			heap.Push(&c.cooldown, &cooldownItem{item: item, nextAllowed: next})
			continue
		}

		if resp != nil && resp.R != nil {
			if len(resp.R.Samples) > 0 {
				c.processSamples(ctx, resp.R.Samples, item)
			}
			if len(resp.R.Nodes) > 0 {
				c.processNodes(ctx, resp.R.Nodes, item.target)
			}
		}

		item.failures = 0
		next := time.Now().Add(computeInterval(resp))
		c.seen[item.node.ID] = next
		heap.Push(&c.cooldown, &cooldownItem{item: item, nextAllowed: next})

		nodesReturned := 0
		if resp != nil && resp.R != nil {
			nodesReturned = len(resp.R.Nodes) / 26
		}
		log.Debug("query complete",
			"node", item.node.Addr.String(),
			"ready_size", c.ready.Len(),
			"cooldown_size", c.cooldown.Len(),
			"nodes_returned", nodesReturned,
			"table_size", c.server.table.NodeCount(),
		)
	}
}

// seedQueue pushes the k closest routing-table nodes to the ready heap.
// Nodes already in seen (currently in cooldownHeap) are skipped.
func (c *crawlerInstance) seedQueue(target NodeID) {
	for _, n := range c.server.table.Closest(target, c.traversalWidth) {
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
// The seen entry is intentionally retained after the pop: the node is now
// in-flight and seen acts as the dedup guard that prevents seedQueue and
// processNodes from re-inserting it while the query is outstanding. The
// caller is responsible for updating seen with the new cooldown time once
// the query completes.
func (c *crawlerInstance) nextEligible(now time.Time) *traversalItem {
	c.promoteReady(now)

	if c.ready.Len() == 0 {
		return nil
	}

	return heap.Pop(&c.ready).(*traversalItem)
}

// processSamples decodes the raw 20-byte infohash samples from a BEP-51
// response. For each new infohash, it launches a goroutine that does a
// get_peers query to the source node to find actual swarm peers, then emits
// DiscoveredPeerss with those peer addresses. Using the DHT node's address
// directly would fail — DHT nodes are UDP-only and don't serve BEP-09 metadata.
func (c *crawlerInstance) processSamples(ctx context.Context, samples string, item *traversalItem) {
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
		case c.discoveryQueue <- discoveryWork{h, item.node}:
		default:
			dropped++
			c.droppedDiscoveries.Add(1)
		}
	}
	if c.rec != nil {
		c.rec.SetDHTQueueCapacity(float64(cap(c.discoveryQueue)))
		if new > 0 {
			c.rec.IncDHTNodesDiscoveredTotal("inserted")
		}
		if total-new-dropped > 0 {
			c.rec.IncDHTNodesDiscoveredTotal("duplicate")
		}
		if dropped > 0 {
			c.rec.IncDHTDiscoveryQueueDroppedTotal()
		}
	}
	logger.FromContext(ctx).Debug("samples processed",
		"service", "crawler",
		"node", item.node.Addr.String(),
		"total", total,
		"new", new,
	)
}

// discoveryWorker consumes infohash work items from the bounded discoveryQueue
// and runs a get_peers lookup for each. Worker count is the concurrency limit.
func (c *discoveryWorker) discover(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case work := <-c.discoveryQueue:
			c.rec.SetDHTDiscoveryQueueDepth(float64(len(c.discoveryQueue)))
			c.discoverPeers(ctx, work.infohash, work.sourceNode)
		}
	}
}

// discoverPeers performs an iterative BEP-05 get_peers lookup for h.
// At each iteration it queries up to Alpha nodes in parallel, then trims the
// shortlist to the k-closest seen so far. It stops early when the closest node
// stops changing (Kademlia convergence) or no new nodes are returned.
func (c *discoveryWorker) discoverPeers(ctx context.Context, h [20]byte, initialNode *Node) {
	log := logger.FromContext(ctx).With("service", "crawler", "infohash", hex.EncodeToString(h[:]))
	if initialNode != nil {
		log.Debug("discovering peers", "initial_node", initialNode.Addr.String())
	}

	start := time.Now()
	defer func() {
		c.rec.ObserveDiscoveryDurationSeconds(time.Since(start).Seconds())
	}()

	target := NodeID(h)
	shortlist := make(map[NodeID]*Node)
	if initialNode != nil {
		shortlist[initialNode.ID] = initialNode
	}

	queried := make(map[NodeID]bool)
	var prevClosest NodeID

	for iter := 0; iter < c.maxIterations; iter++ {
		entries := c.sortByDistance(shortlist, target)
		if len(entries) == 0 {
			if c.rec != nil {
				c.rec.IncDiscoveryWorkItemsTotal("no_peers")
			}
			log.Debug("peer discovery complete", "iterations", iter, "result", "empty_shortlist")
			return
		}

		if iter > 0 && entries[0].ID == prevClosest {
			if c.rec != nil {
				c.rec.IncDiscoveryWorkItemsTotal("success")
			}
			log.Debug("peer discovery complete", "iterations", iter, "result", "converged")
			return
		}
		prevClosest = entries[0].ID

		var toQuery []*Node
		for _, n := range entries {
			if !queried[n.ID] {
				toQuery = append(toQuery, n)
			}
		}
		if len(toQuery) == 0 {
			if c.rec != nil {
				c.rec.IncDiscoveryWorkItemsTotal("no_peers")
			}
			log.Debug("peer discovery complete", "iterations", iter, "result", "all_queried")
			return
		}

		responses := c.queryParallel(ctx, toQuery, h)

		alpha := min(len(toQuery), c.alpha)
		for i := range alpha {
			queried[toQuery[i].ID] = true
		}

		foundPeers, newNodes := c.processResponses(ctx, responses, h)
		if foundPeers {
			if c.rec != nil {
				c.rec.IncDiscoveryWorkItemsTotal("success")
			}
			log.Debug("peer discovery complete", "iterations", iter+1, "result", "peers_found")
			return
		}
		if len(newNodes) == 0 {
			if c.rec != nil {
				c.rec.IncDiscoveryWorkItemsTotal("no_peers")
			}
			log.Debug("peer discovery complete", "iterations", iter+1, "result", "no_new_nodes")
			return
		}

		shortlist = c.trimToKClosest(c.mergeNodes(shortlist, newNodes), target)
	}
	if c.rec != nil {
		c.rec.IncDiscoveryWorkItemsTotal("no_peers")
	}
	log.Debug("peer discovery complete", "iterations", c.maxIterations, "result", "max_iterations")
}

// trimReadyToK sorts the ready heap by XOR distance and discards all items
// beyond traversalWidth, keeping only the k closest nodes. This enforces the
// Kademlia k-shortlist invariant: closer nodes discovered in a response can
// displace farther ones already queued, and the heap never grows without bound.
func (c *crawlerInstance) trimReadyToK() {
	if c.ready.Len() <= c.traversalWidth {
		return
	}
	sort.Sort(&c.ready)
	c.ready = c.ready[:c.traversalWidth]
	heap.Init(&c.ready)
}

// trimToKClosest returns a new map containing only the k nodes closest to target.
func (c *crawler) trimToKClosest(nodes map[NodeID]*Node, target NodeID) map[NodeID]*Node {
	if len(nodes) <= c.traversalWidth {
		return nodes
	}
	sorted := c.sortByDistance(nodes, target)
	result := make(map[NodeID]*Node, c.traversalWidth)
	for _, n := range sorted[:c.traversalWidth] {
		result[n.ID] = n
	}
	return result
}

func (c *crawler) sortByDistance(nodes map[NodeID]*Node, target NodeID) []*Node {
	entries := make([]*Node, 0, len(nodes))
	for _, n := range nodes {
		entries = append(entries, n)
	}

	sort.Slice(entries, func(i, j int) bool {
		di := target.XOR(entries[i].ID)
		dj := target.XOR(entries[j].ID)
		return bytes.Compare(di[:], dj[:]) < 0
	})

	return entries
}

// queryParallel sends BEP-05 get_peers queries to up to Alpha nodes concurrently
// (BEP-05 §2 alpha-parallel lookup) and returns all successful responses.
func (c *discoveryWorker) queryParallel(ctx context.Context, nodes []*Node, h [20]byte) []*Msg {
	alpha := min(len(nodes), c.alpha)

	respCh := make(chan *Msg, alpha)
	var wg sync.WaitGroup

	for i := 0; i < alpha; i++ {
		node := nodes[i]
		wg.Add(1)
		go c.queryNodeAsync(ctx, node, h, respCh, &wg)
	}

	wg.Wait()
	close(respCh)

	var results []*Msg
	for resp := range respCh {
		results = append(results, resp)
	}
	return results
}

// queryNodeAsync sends a single BEP-05 get_peers query (BEP-05 §4.2) to node
// and forwards a successful response to respCh.
func (c *discoveryWorker) queryNodeAsync(ctx context.Context, node *Node, h [20]byte, respCh chan<- *Msg, wg *sync.WaitGroup) {
	defer wg.Done()

	c.rec.IncCrawlerQueriesTotal("get_peers", "discovery", c.id)

	qCtx, cancel := context.WithTimeout(ctx, c.transactionTimeout)
	defer cancel()

	resp, err := c.server.Query(qCtx, node.Addr, node.ID, &Msg{
		Y: "q",
		Q: "get_peers",
		A: &MsgArgs{
			ID:       string(c.server.ourID[:]),
			InfoHash: string(h[:]),
		},
	})
	if err == nil && resp != nil {
		respCh <- resp
	}
}

// processResponses inspects get_peers responses per BEP-05 §4.2: if any
// response carries peer Values, all peer addresses are collected across every
// response and emitted as a single DiscoveredPeers event (more peers increases
// the chance of a successful metadata fetch given the high failure rate observed
// in production). Nodes are returned for the next shortlist iteration.
func (c *crawler) processResponses(ctx context.Context, responses []*Msg, h [20]byte) (bool, map[NodeID]*Node) {
	log := logger.FromContext(ctx).With("service", "crawler", "infohash", hex.EncodeToString(h[:]))
	var newNodes map[NodeID]*Node
	var allPeers []PeerAddr

	for _, resp := range responses {
		if resp == nil || resp.R == nil {
			continue
		}

		allPeers = append(allPeers, c.extractPeers(resp, h)...)

		extractedNodes := c.extractNodes(resp)
		if extractedNodes == nil {
			continue
		}
		if newNodes == nil {
			newNodes = make(map[NodeID]*Node)
		}
		maps.Copy(newNodes, extractedNodes)
	}

	if len(allPeers) > 0 {
		select {
		case c.discovered <- DiscoveredPeers{Infohash: h, Peers: allPeers, SeenAt: time.Now()}:
			log.Debug("peers emitted", "count", len(allPeers))
		default:
			log.Debug("discovered channel full, peers dropped", "count", len(allPeers))
		}
		return true, nil
	}

	return false, newNodes
}

// extractPeers decodes the compact 6-byte peer list from a BEP-05 get_peers
// response (BEP-05 §4.2 "values" field). Each entry is a TCP address for the
// BitTorrent peer protocol — not a DHT UDP node address.
// The caller (processResponses) is responsible for aggregating results across
// multiple parallel responses and emitting a single DiscoveredPeers event.
func (c *crawler) extractPeers(resp *Msg, h [20]byte) []PeerAddr {
	if len(resp.R.Values) == 0 {
		return nil
	}

	peers, err := DecodePeers(resp.R.Values)
	if err != nil || len(peers) == 0 {
		return nil
	}

	var peerAddrs []PeerAddr
	for _, peer := range peers {
		if tcpAddr, ok := peer.(*net.TCPAddr); ok {
			peerAddrs = append(peerAddrs, PeerAddr{
				SourceIP: tcpAddr.IP,
				Port:     tcpAddr.Port,
			})
		}
	}

	return peerAddrs
}

func (c *crawler) mergeNodes(shortlist map[NodeID]*Node, newNodes map[NodeID]*Node) map[NodeID]*Node {
	result := make(map[NodeID]*Node, len(shortlist)+len(newNodes))
	maps.Copy(result, shortlist)
	maps.Copy(result, newNodes)
	return result
}

// extractNodes decodes the compact 26-byte node list from a BEP-05 get_peers
// or BEP-51 sample_infohashes response ("nodes" field, BEP-05 §4.2) and
// inserts each node into the routing table.
func (c *crawler) extractNodes(resp *Msg) map[NodeID]*Node {
	if len(resp.R.Nodes) == 0 {
		return nil
	}

	nodes, err := DecodeNodes(resp.R.Nodes)
	if err != nil {
		return nil
	}

	result := make(map[NodeID]*Node)
	for _, n := range nodes {
		result[n.ID] = n
		c.server.table.Insert(n)
	}
	return result
}

// processNodes decodes compact node records from a BEP-51 sample_infohashes
// response ("nodes" field, BEP-51 §4), inserts them into the routing table,
// and enqueues eligible nodes onto the ready heap.
// Nodes already in seen (currently in cooldownHeap) are skipped.
func (c *crawlerInstance) processNodes(ctx context.Context, encoded string, target NodeID) {
	nodes, err := DecodeNodes(encoded)
	if err != nil {
		return
	}
	inserted, queued := 0, 0
	for _, n := range nodes {
		c.server.table.Insert(n)
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
		"table_size", c.server.table.NodeCount(),
	)
}
