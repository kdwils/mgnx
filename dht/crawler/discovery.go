package crawler

import (
	"bytes"
	"context"
	"encoding/hex"
	"maps"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/dht/krpc"
	"github.com/kdwils/mgnx/dht/table"
	"github.com/kdwils/mgnx/dht/types"
	"github.com/kdwils/mgnx/logger"
	"github.com/kdwils/mgnx/recorder"
)

// DiscoveryWork is a work item passed from Crawler to DiscoveryWorker.
type DiscoveryWork struct {
	Infohash [20]byte
	Source   *table.Node
}

// NewDiscoveryWorker constructs a DiscoveryWorker. queue is the read side of the
// shared DiscoveryWork channel.
func NewDiscoveryWorker(id int, dht DHT, queue <-chan DiscoveryWork, cfg config.Crawler, rec *recorder.Recorder) *DiscoveryWorker {
	return &DiscoveryWorker{
		id:    id,
		dht:   dht,
		queue: queue,
		rec:   rec,
		cfg:   cfg,
	}
}

// Start runs the discovery loop, blocking until ctx is cancelled.
func (w *DiscoveryWorker) Start(ctx context.Context) {
	w.discover(ctx)
}

// discover consumes infohash work items from the bounded queue.
func (w *DiscoveryWorker) discover(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case work := <-w.queue:
			w.rec.SetDHTDiscoveryQueueDepth(float64(len(w.queue)))
			w.rec.AddDiscoveryWorkersBusy(1)
			w.discoverPeers(ctx, work.Infohash, work.Source)
			w.rec.AddDiscoveryWorkersBusy(-1)
		}
	}
}

// discoverPeers performs an iterative BEP-05 get_peers lookup for h.
func (w *DiscoveryWorker) discoverPeers(ctx context.Context, h [20]byte, initialNode *table.Node) {
	log := logger.FromContext(ctx).With("service", "crawler", "infohash", hex.EncodeToString(h[:]))
	if initialNode != nil {
		log.Debug("discovering peers", "initial_node", initialNode.Addr.String())
	}

	start := time.Now()
	defer func() {
		w.rec.ObserveDiscoveryDurationSeconds(time.Since(start).Seconds())
	}()

	target := table.NodeID(h)
	shortlist := make(map[table.NodeID]*table.Node)
	if initialNode != nil {
		shortlist[initialNode.ID] = initialNode
	}

	queried := make(map[table.NodeID]bool)

	var collectedPeers []types.PeerAddr
	result := "max_iterations"

	for iter := 0; iter < w.cfg.DiscoveryMaxIterations; iter++ {
		entries := w.sortByDistance(shortlist, target)
		if len(entries) == 0 {
			result = "empty_shortlist"
			break
		}

		var toQuery []*table.Node
		for _, n := range entries {
			if !queried[n.ID] {
				toQuery = append(toQuery, n)
			}
		}
		if len(toQuery) == 0 {
			result = "all_queried"
			break
		}

		dispatched := toQuery[:min(len(toQuery), w.cfg.Alpha)]
		for _, n := range dispatched {
			queried[n.ID] = true
		}

		responses := w.queryParallel(ctx, toQuery, h)

		peers, newNodes := w.processResponses(ctx, responses)
		collectedPeers = append(collectedPeers, peers...)

		if len(newNodes) == 0 {
			continue
		}
		shortlist = w.trimToKClosest(w.mergeNodes(shortlist, newNodes), target)
	}

	if len(collectedPeers) == 0 {
		if w.rec != nil {
			w.rec.IncDiscoveryWorkItemsTotal("no_peers")
		}
		log.Debug("peer discovery complete", "result", result)
		return
	}

	w.dht.Emit(types.DiscoveredPeers{Infohash: h, Peers: collectedPeers, SeenAt: time.Now()})
	if w.rec != nil {
		w.rec.IncDiscoveryWorkItemsTotal("success")
	}
	log.Debug("peer discovery complete", "result", result, "peers", len(collectedPeers))
}

// trimToKClosest returns a new map containing only the k nodes closest to target.
func (w *DiscoveryWorker) trimToKClosest(nodes map[table.NodeID]*table.Node, target table.NodeID) map[table.NodeID]*table.Node {
	if len(nodes) <= w.cfg.TraversalWidth {
		return nodes
	}
	sorted := w.sortByDistance(nodes, target)
	result := make(map[table.NodeID]*table.Node, w.cfg.TraversalWidth)
	for _, n := range sorted[:w.cfg.TraversalWidth] {
		result[n.ID] = n
	}
	return result
}

func (w *DiscoveryWorker) sortByDistance(nodes map[table.NodeID]*table.Node, target table.NodeID) []*table.Node {
	entries := make([]*table.Node, 0, len(nodes))
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

// queryParallel sends BEP-05 get_peers queries to up to Alpha nodes concurrently.
func (w *DiscoveryWorker) queryParallel(ctx context.Context, nodes []*table.Node, h [20]byte) []*krpc.Msg {
	alpha := min(len(nodes), w.cfg.Alpha)

	respCh := make(chan *krpc.Msg, alpha)
	var wg sync.WaitGroup

	for i := range alpha {
		node := nodes[i]
		wg.Add(1)
		go w.queryNodeAsync(ctx, node, h, respCh, &wg)
	}

	wg.Wait()
	close(respCh)

	var results []*krpc.Msg
	for resp := range respCh {
		results = append(results, resp)
	}
	return results
}

// queryNodeAsync sends a single BEP-05 get_peers query and forwards the response.
func (w *DiscoveryWorker) queryNodeAsync(ctx context.Context, node *table.Node, h [20]byte, respCh chan<- *krpc.Msg, wg *sync.WaitGroup) {
	defer wg.Done()

	w.rec.IncCrawlerQueriesTotal("get_peers", "discovery", w.id)

	resp, err := w.dht.GetPeers(ctx, node.Addr, node.ID, table.NodeID(h))
	if err == nil && resp != nil {
		respCh <- resp
	}
}

// processResponses inspects get_peers responses and returns collected peers and new nodes.
func (w *DiscoveryWorker) processResponses(ctx context.Context, responses []*krpc.Msg) ([]types.PeerAddr, map[table.NodeID]*table.Node) {
	var newNodes map[table.NodeID]*table.Node
	var allPeers []types.PeerAddr

	for _, resp := range responses {
		if resp == nil || resp.R == nil {
			continue
		}

		allPeers = append(allPeers, w.extractPeers(resp)...)

		extractedNodes := w.extractNodes(ctx, resp)
		if extractedNodes == nil {
			continue
		}
		if newNodes == nil {
			newNodes = make(map[table.NodeID]*table.Node)
		}
		maps.Copy(newNodes, extractedNodes)
	}

	return allPeers, newNodes
}

// extractPeers decodes the compact 6-byte peer list from a BEP-05 get_peers response.
func (w *DiscoveryWorker) extractPeers(resp *krpc.Msg) []types.PeerAddr {
	if len(resp.R.Values) == 0 {
		return nil
	}

	peers, err := table.DecodePeers(resp.R.Values)
	if err != nil || len(peers) == 0 {
		return nil
	}

	var peerAddrs []types.PeerAddr
	for _, peer := range peers {
		if tcpAddr, ok := peer.(*net.TCPAddr); ok {
			peerAddrs = append(peerAddrs, types.PeerAddr{
				SourceIP: tcpAddr.IP,
				Port:     tcpAddr.Port,
			})
		}
	}

	return peerAddrs
}

func (w *DiscoveryWorker) mergeNodes(shortlist map[table.NodeID]*table.Node, newNodes map[table.NodeID]*table.Node) map[table.NodeID]*table.Node {
	result := make(map[table.NodeID]*table.Node, len(shortlist)+len(newNodes))
	maps.Copy(result, shortlist)
	maps.Copy(result, newNodes)
	return result
}

// extractNodes decodes the compact 26-byte node list from a response and inserts
// each node into the routing table.
func (w *DiscoveryWorker) extractNodes(ctx context.Context, resp *krpc.Msg) map[table.NodeID]*table.Node {
	if len(resp.R.Nodes) == 0 {
		return nil
	}

	nodes, err := table.DecodeNodes(resp.R.Nodes)
	if err != nil {
		return nil
	}

	result := make(map[table.NodeID]*table.Node)
	for _, n := range nodes {
		result[n.ID] = n
		w.dht.InsertNode(ctx, n)
	}
	return result
}