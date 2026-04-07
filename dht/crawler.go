package dht

//go:generate go run go.uber.org/mock/mockgen -destination=../mocks/mock_crawler.go -package=mocks github.com/kdwils/mgnx/dht Crawler

import (
	"bytes"
	"container/heap"
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/logger"
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

// Crawler is the public interface for the DHT crawler.
type Crawler interface {
	Infohashes() <-chan DiscoveredPeers
	Start(ctx context.Context) error
	Stop(ctx context.Context)
}

// crawler wraps Server and drives two discovered modes:
//  1. Passive: announce_peer queries already wired in the server (Step 6).
//  2. Active:  BEP-51 sample_infohashes traversal driven here.
type crawler struct {
	server      *Server
	discovered  chan DiscoveredPeers
	dedup       *BloomFilter
	cfg         config.DHT
	crawlerCfg  config.Crawler
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	rateLimiter *nodeRateLimiter
}

// nodeRateLimiter enforces minimum spacing between queries to the same node.
type nodeRateLimiter struct {
	mu         sync.Mutex
	lastQuery  map[NodeID]time.Time
	minSpacing time.Duration
}

func newNodeRateLimiter() *nodeRateLimiter {
	return &nodeRateLimiter{
		lastQuery:  make(map[NodeID]time.Time),
		minSpacing: 2 * time.Second,
	}
}

func (r *nodeRateLimiter) throttle(id NodeID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if last, ok := r.lastQuery[id]; ok {
		if time.Since(last) < r.minSpacing {
			return true
		}
	}
	r.lastQuery[id] = time.Now()
	return false
}

// NewCrawler creates a Crawler backed by a new UDP Server. The BloomFilter is
// created here and wired into the server so that both the passive
// announce_peer path and the active BEP-51 path share the same dedup state.
func NewCrawler(dhtCfg config.DHT, crawlerCfg config.Crawler) (Crawler, error) {
	server, err := NewServer(dhtCfg)
	if err != nil {
		return nil, err
	}
	dedup := NewBloomFilter()
	server.dedup = dedup
	return &crawler{
		server:      server,
		discovered:  server.discovered,
		dedup:       dedup,
		cfg:         dhtCfg,
		crawlerCfg:  crawlerCfg,
		rateLimiter: newNodeRateLimiter(),
	}, nil
}

// Infohashes returns the channel of discovereded infohash events.
func (c *crawler) Infohashes() <-chan DiscoveredPeers {
	return c.discovered
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

	workers := c.crawlerCfg.Workers
	for range workers {
		c.wg.Go(func() {
			c.crawlerWorker(crawlCtx)
		})
	}

	logger.FromContext(ctx).Info("DHT crawler started", "crawler_workers", workers)
	return nil
}

// Stop cancels the BEP-51 workers, waits for them to exit, then shuts down
// the server (closing the socket and persisting the routing table).
func (c *crawler) Stop(ctx context.Context) {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	c.server.Stop(ctx)
}

// traversalItem is an element of the traversal heap.
type traversalItem struct {
	node   *Node
	target NodeID
	dist   NodeID // XOR(node.ID, target); used for min-heap ordering
	index  int
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

// retargetEvery is the number of queries each worker issues before
// randomising its target to spread coverage across the full keyspace.
const retargetEvery = 200

// crawlerWorker implements the BEP-51 active traversal loop. It maintains its
// own priority queue (ordered by XOR distance to the current target) and a
// per-node cooldown map so it respects the Interval returned by each node.
// When the queue is exhausted the worker picks a new random target and
// re-seeds from the routing table.
func (c *crawler) crawlerWorker(ctx context.Context) {
	var target NodeID
	rand.Read(target[:])

	pq := &traversalHeap{}
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

		// Per-node rate limiting to avoid overwhelming responsive nodes
		if c.rateLimiter.throttle(item.node.ID) {
			heap.Push(pq, item)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		qCtx, cancel := context.WithTimeout(ctx, c.cfg.TransactionTimeout)

		// Try BEP-05 (get_peers) first - all DHT nodes support it
		resp, err := c.server.Query(qCtx, item.node.Addr, item.node.ID, &Msg{
			Y: "q",
			Q: "get_peers",
			A: &MsgArgs{
				ID:       string(c.server.ourID[:]),
				InfoHash: string(item.target[:]),
			},
		})

		// Fallback to BEP-51 if get_peers returned no peers (only nodes)
		if err == nil && resp != nil && resp.R != nil && len(resp.R.Values) == 0 {
			resp, err = c.server.Query(qCtx, item.node.Addr, item.node.ID, &Msg{
				Y: "q",
				Q: "sample_infohashes",
				A: &MsgArgs{
					ID:     string(c.server.ourID[:]),
					Target: string(item.target[:]),
				},
			})
		}
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
			// BEP-51: process samples (infohashes)
			if len(resp.R.Samples) > 0 {
				c.processSamples(ctx, resp.R.Samples, item)
			}
			// Both protocols return nodes for continuation
			if len(resp.R.Nodes) > 0 {
				c.processNodes(resp.R.Nodes, item.target, pq)
			}
		}

		seen[item.node.ID] = time.Now().Add(interval)
	}
}

// seedQueue pushes the k closest routing-table nodes to target onto pq.
func (c *crawler) seedQueue(pq *traversalHeap, target NodeID) {
	k := c.cfg.BucketSize
	if k <= 0 {
		k = 8
	}
	for _, n := range c.server.table.Closest(target, k) {
		heap.Push(pq, &traversalItem{
			node:   n,
			target: target,
			dist:   target.XOR(n.ID),
		})
	}
}

// nextEligible pops items from pq until it finds one not in the cooldown
// window. Returns nil if the queue is empty.
func (c *crawler) nextEligible(pq *traversalHeap, seen map[NodeID]time.Time) *traversalItem {
	for pq.Len() > 0 {
		item := heap.Pop(pq).(*traversalItem)
		if t, ok := seen[item.node.ID]; !ok || time.Now().After(t) {
			return item
		}
	}
	return nil
}

// processSamples decodes the raw 20-byte infohash samples from a BEP-51
// response. For each new infohash, it launches a goroutine that does a
// get_peers query to the source node to find actual swarm peers, then emits
// DiscoveredPeerss with those peer addresses. Using the DHT node's address
// directly would fail — DHT nodes are UDP-only and don't serve BEP-09 metadata.
func (c *crawler) processSamples(ctx context.Context, samples string, item *traversalItem) {
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
		go c.discoverPeers(ctx, h, item.node)
	}
	logger.FromContext(ctx).Debug("samples processed",
		"node", item.node.Addr,
		"total", total,
		"new", new,
	)
}

// discoverPeers performs an iterative get_peers lookup to find peers for infohash.
// Per BEP-05, when a node doesn't have peers it returns closer nodes - we must
// iteratively query them until peers are found.
func (c *crawler) discoverPeers(ctx context.Context, h [20]byte, initialNode *Node) {
	log := logger.FromContext(ctx)
	target := NodeID(h)

	log.Debug("discoverPeers started", "infohash", hex.EncodeToString(h[:]), "initialNode", initialNode != nil)

	shortlist := make(map[NodeID]*Node)
	if initialNode != nil {
		shortlist[initialNode.ID] = initialNode
	}

	for iter := 0; iter < c.cfg.MaxIterations; iter++ {
		entries := c.sortByDistance(shortlist, target)
		if len(entries) == 0 {
			return
		}

		responses := c.queryParallel(ctx, entries, target, h)

		foundPeers, newNodes := c.processResponses(responses, h)

		if foundPeers {
			return
		}

		if len(newNodes) == 0 {
			return
		}

		shortlist = c.mergeNodes(shortlist, newNodes)
	}
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

func (c *crawler) queryParallel(ctx context.Context, nodes []*Node, target NodeID, h [20]byte) []*Msg {
	alpha := c.cfg.Alpha
	if len(nodes) < alpha {
		alpha = len(nodes)
	}

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

func (c *crawler) queryNodeAsync(ctx context.Context, node *Node, h [20]byte, respCh chan<- *Msg, wg *sync.WaitGroup) {
	defer wg.Done()

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
	if err == nil && resp != nil {
		respCh <- resp
	}
}

func (c *crawler) processResponses(responses []*Msg, h [20]byte) (bool, map[NodeID]*Node) {
	var newNodes map[NodeID]*Node

	for _, resp := range responses {
		if resp == nil || resp.R == nil {
			continue
		}

		if peers := c.extractPeers(resp, h); peers != nil {
			return true, nil
		}

		extractedNodes := c.extractNodes(resp)
		if extractedNodes == nil {
			continue
		}
		if newNodes == nil {
			newNodes = make(map[NodeID]*Node)
		}
		for id, node := range extractedNodes {
			newNodes[id] = node
		}
	}

	return false, newNodes
}

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

	if len(peerAddrs) == 0 {
		return nil
	}

	select {
	case c.discovered <- DiscoveredPeers{Infohash: h, Peers: peerAddrs, SeenAt: time.Now()}:
	default:
	}

	return peerAddrs
}

func (c *crawler) mergeNodes(shortlist map[NodeID]*Node, newNodes map[NodeID]*Node) map[NodeID]*Node {
	result := make(map[NodeID]*Node, len(shortlist)+len(newNodes))
	for id, node := range shortlist {
		result[id] = node
	}
	for id, node := range newNodes {
		result[id] = node
	}
	return result
}

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

// processNodes decodes compact node records from a BEP-51 response, inserts
// them into the routing table, and enqueues them for future traversal.
func (c *crawler) processNodes(encoded string, target NodeID, pq *traversalHeap) {
	nodes, err := DecodeNodes(encoded)
	if err != nil {
		return
	}
	for _, n := range nodes {
		c.server.table.Insert(n)
		heap.Push(pq, &traversalItem{
			node:   n,
			target: target,
			dist:   target.XOR(n.ID),
		})
	}
}
