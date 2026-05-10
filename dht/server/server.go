package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/dht/filter"
	"github.com/kdwils/mgnx/dht/krpc"
	"github.com/kdwils/mgnx/dht/table"
	"github.com/kdwils/mgnx/dht/types"
	"github.com/kdwils/mgnx/logger"
	"github.com/kdwils/mgnx/recorder"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

//go:generate go run go.uber.org/mock/mockgen -destination=mocks/conn/mock_conn.go -package=mocks github.com/kdwils/mgnx/dht Conn

// Conn is the injectable UDP socket boundary. *net.UDPConn is the real implementation.
type Conn interface {
	ReadFromUDP(b []byte) (int, *net.UDPAddr, error)
	WriteToUDP(b []byte, addr *net.UDPAddr) (int, error)
	LocalAddr() net.Addr
	Close() error
	SetDeadline(t time.Time) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
}

type outMsg struct {
	addr *net.UDPAddr
	msg  *krpc.Msg
}

type inMsg struct {
	addr *net.UDPAddr
	msg  *krpc.Msg
}

// Server is the UDP DHT node. It owns the socket, routing table, transaction
// manager, token manager, and handles discovered peers.
type Server struct {
	conn                   Conn
	nodeID                 table.NodeID
	table                  *table.RoutingTable
	txns                   *TxnManager
	outbound               chan *outMsg
	discovered             chan types.DiscoveredPeers
	token                  *TokenManager
	dedup                  *filter.BloomFilter
	peerStore              *PeerStore
	rec                    *recorder.Recorder
	rate                   *rate.Limiter
	ipLimiter              *ipLimiter
	handlers               chan inMsg
	Resolver               Resolver
	workers                int
	transactionTimeout     time.Duration
	bucketSize             int
	maxNodesPerResponse    int
	maxPeersPerResponse    int
	bucketRefreshInterval  time.Duration
	bootstrapNodes         []string
	warmBootstrapThreshold int
	nodesPath              string
	wg                     sync.WaitGroup
}

// NewServer initialises all subsystems. conn must be a bound UDP socket;
// cmd/serve.go calls net.ListenUDP and passes the result.
// Call Start to launch the internal goroutines and bootstrap the routing table.
func NewServer(cfg config.DHT, ip net.IP, conn Conn, rec *recorder.Recorder, discovered chan types.DiscoveredPeers, dedup *filter.BloomFilter) (*Server, error) {
	nodeID, err := getServerNodeID(cfg, ip)
	if err != nil {
		return nil, err
	}

	s := &Server{
		conn:                   conn,
		nodeID:                 nodeID,
		outbound:               make(chan *outMsg, 512),
		discovered:             discovered,
		token:                  NewTokenManager(cfg.TokenRotation),
		dedup:                  dedup,
		peerStore:              newPeerStore(cfg.PeerStoreMaxEntries, cfg.PeerStoreMaxPeersPerHash, cfg.PeerStoreTTL),
		rec:                    rec,
		rate:                   rate.NewLimiter(rate.Limit(cfg.RateLimit), cfg.RateBurst),
		ipLimiter:              newIPLimiter(cfg.RateLimit, cfg.RateBurst, 5*time.Minute, cfg.IPLimiterMaxSize),
		workers:                cfg.Workers,
		handlers:               make(chan inMsg, 512),
		transactionTimeout:     cfg.TransactionTimeout,
		maxNodesPerResponse:    cfg.MaxNodesPerResponse,
		maxPeersPerResponse:    cfg.MaxPeersPerResponse,
		bucketSize:             cfg.BucketSize,
		bucketRefreshInterval:  cfg.BucketRefreshInterval,
		bootstrapNodes:         cfg.BootstrapNodes,
		warmBootstrapThreshold: cfg.WarmBootstrapThreshold,
		nodesPath:              cfg.NodesPath,
		Resolver:               net.DefaultResolver,
	}

	s.table = table.NewRoutingTable(nodeID, cfg, s)
	s.txns = NewTxnManager(s.table, cfg.TransactionTimeout)

	return s, nil
}

// Start launches all internal goroutines and bootstraps the routing table.
// It performs a warm restart (ping existing nodes) if the table is already
// populated, falling back to a cold bootstrap from cfg.BootstrapNodes.
// krpc.Returns when the routing table is populated, or an error if bootstrap fails.
// If BootstrapNodes is empty, starts goroutines and returns immediately.
func (s *Server) Start(ctx context.Context) error {
	s.txns.Start(ctx)
	s.token.Start(ctx)
	s.ipLimiter.Start(ctx)
	s.rec.SetRoutingTableSize(float64(s.table.NodeCount()))

	if s.nodesPath != "" {
		if err := s.loadRoutingTable(ctx); err != nil {
			logger.FromContext(ctx).Warn("failed to load routing table", "error", err, "path", s.nodesPath)
		}
	}

	s.wg.Go(func() { s.peerStore.startCleanup(ctx) })
	s.wg.Go(func() { s.readLoop(ctx) })
	s.wg.Go(func() { s.writeLoop(ctx) })
	for i := 0; i < s.workers; i++ {
		s.wg.Go(func() { s.queryHandlerLoop(ctx) })
	}
	s.wg.Go(func() { s.bucketRefreshLoop(ctx) })

	logger.FromContext(ctx).Info("server started",
		"service", "dht",
		"addr", s.conn.LocalAddr().String(),
		"node_id", hex.EncodeToString(s.nodeID[:]),
		"workers", s.workers,
	)

	if len(s.bootstrapNodes) == 0 {
		return nil
	}

	if !s.warmRestart(ctx) {
		if err := s.Bootstrap(ctx, s.bootstrapNodes); err != nil {
			return err
		}
	}

	if s.table.NodeCount() == 0 {
		return fmt.Errorf("bootstrap failed: no nodes in routing table")
	}

	return nil
}

// warmRestart pings all nodes currently in the routing table and returns true
// if at least warmBootstrapThreshold respond.
func (s *Server) warmRestart(ctx context.Context) bool {
	log := logger.FromContext(ctx).With("service", "dht")
	loaded := s.table.NodeCount()
	if loaded == 0 {
		return false
	}
	const maxWarmPingNodes = 200
	sample := min(loaded, maxWarmPingNodes)
	nodes := s.table.Closest(s.nodeID, sample)
	live := s.pingNodes(ctx, nodes)
	log.Info("warm restart ping complete", "loaded", loaded, "live", live, "threshold", s.warmBootstrapThreshold)
	if live < s.warmBootstrapThreshold {
		return false
	}
	s.refreshStaleBuckets(ctx)
	return true
}

func getServerNodeID(cfg config.DHT, externalIP net.IP) (table.NodeID, error) {
	if externalIP != nil {
		return table.DeriveNodeIDFromIP(externalIP)
	}
	if cfg.NodeID != "" {
		return table.ParseNodeIDHex(cfg.NodeID)
	}

	var id table.NodeID
	_, err := rand.Read(id[:])
	return id, err
}

func (s *Server) Stop(ctx context.Context) error {
	logger := logger.FromContext(ctx)
	if s.nodesPath != "" {
		if err := s.saveRoutingTable(); err != nil {
			logger.Error("failed to save routing table", "error", err, "path", s.nodesPath)
		}
	}
	err := s.conn.Close()
	if err != nil {
		logger.Error("failed to close udp server conn", "error", err)
	}
	return nil
}

func (s *Server) loadRoutingTable(ctx context.Context) error {
	data, err := os.ReadFile(s.nodesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var state table.RoutingTableState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}

	if err := s.table.LoadState(ctx, state); err != nil {
		return err
	}

	logger.FromContext(ctx).Info("routing table loaded",
		"path", s.nodesPath,
		"nodes", s.table.NodeCount(),
		"exact_buckets", state.NodeID == s.nodeID,
		"loaded_buckets", len(state.Buckets),
	)
	return nil
}


func (s *Server) saveRoutingTable() error {
	state := s.table.SaveState()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(s.nodesPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(s.nodesPath, data, 0644)
}

// InsertNode adds a node to the routing table.
func (s *Server) InsertNode(ctx context.Context, node *table.Node) {
	s.table.Insert(ctx, node)
}

// Closest returns the n nodes closest to target in the routing table.
func (s *Server) Closest(target table.NodeID, n int) []*table.Node { return s.table.Closest(target, n) }

// NodeCount returns the total number of nodes in the routing table.
func (s *Server) NodeCount() int { return s.table.NodeCount() }

// NodeID returns the server's node ID.
func (s *Server) NodeID() table.NodeID { return s.nodeID }

// Addr returns the server's local UDP address.
func (s *Server) Addr() *net.UDPAddr {
	return s.conn.LocalAddr().(*net.UDPAddr)
}

// MarkSuccess marks a node as having responded successfully.
func (s *Server) MarkSuccess(id table.NodeID) { s.table.MarkSuccess(id) }

// MarkFailure marks a node as having failed to respond.
func (s *Server) MarkFailure(id table.NodeID) { s.table.MarkFailure(id) }

// Emit enqueues a types.DiscoveredPeers event on the discovered channel.
// Drops silently if the channel is full.
func (s *Server) Emit(event types.DiscoveredPeers) {
	select {
	case s.discovered <- event:
	default:
	}
}

// RefreshStaleBuckets triggers an immediate stale-bucket refresh. Intended for testing.
func (s *Server) RefreshStaleBuckets(ctx context.Context) { s.refreshStaleBuckets(ctx) }

// pingNodes pings nodes concurrently (up to 32 in-flight) and returns the
// number that responded within transactionTimeout.
func (s *Server) pingNodes(ctx context.Context, nodes []*table.Node) int {
	const maxConcurrency = 32
	sem := make(chan struct{}, maxConcurrency)

	var mu sync.Mutex
	live := 0

	eg, egCtx := errgroup.WithContext(ctx)
	for _, n := range nodes {
		eg.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()

			pingCtx, cancel := context.WithTimeout(egCtx, s.transactionTimeout)
			defer cancel()
			resp, err := s.Ping(pingCtx, n.Addr, n.ID)
			if err != nil || resp == nil || resp.R == nil {
				return nil
			}
			mu.Lock()
			live++
			mu.Unlock()
			return nil
		})
	}
	eg.Wait()
	return live
}

// readLoop reads datagrams from the socket and routes them.
func (s *Server) readLoop(ctx context.Context) {
	for {
		buf := make([]byte, 2048)
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			return
		}

		s.rec.IncDHTPacketsInTotal()

		var msg krpc.Msg
		if err := bencode.Unmarshal(buf[:n], &msg); err != nil {
			continue
		}
		s.routeMessage(ctx, addr, &msg)
	}
}

// routeMessage routes a decoded message to the transaction map (responses) or
// the query handler pool (queries).
func (s *Server) routeMessage(ctx context.Context, addr *net.UDPAddr, msg *krpc.Msg) {
	if msg == nil {
		return
	}

	switch msg.Y {
	case "r", "e":
		s.txns.Complete(msg.T, msg)
		if msg.Y == "r" && msg.R != nil {
			s.updateTableFromResponse(ctx, addr, msg)
		}
		return
	case "q":
		select {
		case s.handlers <- inMsg{addr: addr, msg: msg}:
		default:
		}
	default:
		return
	}
}

// updateTableFromResponse inserts/refreshes the responding node in the table.
func (s *Server) updateTableFromResponse(ctx context.Context, addr *net.UDPAddr, msg *krpc.Msg) {
	id, err := table.ParseNodeID(msg.R.ID)
	if err != nil {
		return
	}
	node := &table.Node{ID: id, Addr: addr, LastSeen: time.Now()}
	result := s.table.Insert(ctx, node)
	s.rec.IncNodesDiscoveredTotal(result)
	s.rec.SetRoutingTableSize(float64(s.table.NodeCount()))
}

// queryHandlerLoop processes inbound queries until ctx is cancelled.
func (s *Server) queryHandlerLoop(ctx context.Context) {
	for {
		select {
		case in := <-s.handlers:
			s.processQuery(ctx, in)
		case <-ctx.Done():
			return
		}
	}
}

// processQuery dispatches a single inbound KRPC query to the appropriate handler.
func (s *Server) processQuery(ctx context.Context, in inMsg) {
	log := logger.FromContext(ctx).With("service", "dht")

	s.rec.IncMessagesInTotal(in.msg.Q)

	if !s.ipLimiter.Allow(in.addr.IP) {
		return
	}

	if in.msg.A != nil {
		if id, err := table.ParseNodeID(in.msg.A.ID); err == nil {
			node := &table.Node{ID: id, Addr: in.addr, LastSeen: time.Now()}
			result := s.table.Insert(ctx, node)
			s.rec.IncNodesDiscoveredTotal(result)
		}
	}

	switch in.msg.Q {
	case "ping":
		s.handlePing(ctx, in.addr, in.msg)
	case "find_node":
		s.handleFindNode(ctx, in.addr, in.msg)
	case "get_peers":
		s.handleGetPeers(ctx, in.addr, in.msg)
	case "announce_peer":
		s.handleAnnouncePeer(ctx, in.addr, in.msg)
	case "sample_infohashes":
		s.handleSampleInfohashes(ctx, in.addr, in.msg)
	default:
		log.Debug("unknown request", "query", in.msg.Q, "from", in.addr.String())
		return
	}
}

func (s *Server) handlePing(ctx context.Context, addr *net.UDPAddr, msg *krpc.Msg) {
	log := logger.FromContext(ctx).With("service", "dht")
	log.Debug("handle_ping", "from", addr.String())
	s.respond(addr, msg.T, &krpc.Return{ID: string(s.nodeID[:])})
}

func (s *Server) handleFindNode(ctx context.Context, addr *net.UDPAddr, msg *krpc.Msg) {
	log := logger.FromContext(ctx).With("service", "dht")
	log.Debug("handle_find_node", "from", addr.String())

	if msg.A == nil {
		s.respondError(addr, msg.T, krpc.ErrProtocol, "missing arguments")
		return
	}
	target, err := table.ParseNodeID(msg.A.Target)
	if err != nil {
		target, err = table.ParseNodeID(msg.A.InfoHash)
		if err != nil {
			s.respondError(addr, msg.T, krpc.ErrProtocol, "missing target")
			return
		}
	}
	closest := s.table.Closest(target, s.bucketSize)
	maxNodes := s.maxNodesPerResponse
	if len(closest) > maxNodes {
		closest = closest[:maxNodes]
	}
	s.respond(addr, msg.T, &krpc.Return{
		ID:    string(s.nodeID[:]),
		Nodes: table.EncodeNodes(closest),
	})
}

func (s *Server) handleGetPeers(ctx context.Context, addr *net.UDPAddr, msg *krpc.Msg) {
	log := logger.FromContext(ctx).With("service", "dht", "handler", "get_peers", "addr", addr.String())
	log.Debug("received request")

	if msg.A == nil {
		log.Debug("missing arguments")
		s.respondError(addr, msg.T, krpc.ErrProtocol, "missing arguments")
		return
	}
	target, err := table.ParseNodeID(msg.A.InfoHash)
	if err != nil {
		log.Debug("missing info_hash")
		s.respondError(addr, msg.T, krpc.ErrProtocol, "missing info_hash")
		return
	}

	token := s.token.Generate(addr.IP)

	peers := s.peerStore.Get(target)
	if len(peers) > 0 {
		if len(peers) > s.maxPeersPerResponse {
			peers = peers[:s.maxPeersPerResponse]
		}
		values := make([]string, len(peers))
		for i, p := range peers {
			values[i] = table.EncodePeer(p.IP, p.Port)
		}
		s.respond(addr, msg.T, &krpc.Return{
			ID:     string(s.nodeID[:]),
			Values: values,
			Token:  token,
		})
		return
	}

	closest := s.table.Closest(target, s.bucketSize)
	if len(closest) > s.maxNodesPerResponse {
		closest = closest[:s.maxNodesPerResponse]
	}
	s.respond(addr, msg.T, &krpc.Return{
		ID:    string(s.nodeID[:]),
		Nodes: table.EncodeNodes(closest),
		Token: token,
	})
}

func (s *Server) handleAnnouncePeer(ctx context.Context, addr *net.UDPAddr, msg *krpc.Msg) {
	log := logger.FromContext(ctx).With("service", "dht", "handler", "announce_peer", "addr", addr.String())
	log.Debug("received request")

	if msg.A == nil {
		log.Debug("missing arguments")
		s.respondError(addr, msg.T, krpc.ErrProtocol, "missing arguments")
		return
	}
	if !s.token.Validate(addr.IP, msg.A.Token) {
		log.Debug("invalid token")
		s.respondError(addr, msg.T, krpc.ErrProtocol, "bad token")
		return
	}

	var h table.NodeID
	copy(h[:], msg.A.InfoHash)

	port := msg.A.Port
	if msg.A.ImpliedPort != nil && *msg.A.ImpliedPort != 0 {
		port = addr.Port
	}
	if port <= 0 || port > 65535 {
		s.respond(addr, msg.T, &krpc.Return{ID: string(s.nodeID[:])})
		return
	}

	s.peerStore.Add(h, addr.IP, port)

	if s.dedup != nil && s.dedup.SeenOrAdd(h) {
		s.respond(addr, msg.T, &krpc.Return{ID: string(s.nodeID[:])})
		return
	}

	event := types.DiscoveredPeers{
		Infohash: h,
		Peers:    []types.PeerAddr{{SourceIP: addr.IP, Port: port}},
		SeenAt:   time.Now(),
	}

	select {
	case s.discovered <- event:
		log.Debug("peer discovered", "infohash", hex.EncodeToString(h[:]), "from", addr.String())
	default:
		log.Debug("peer dropped due to full discovered channel", "infohash", hex.EncodeToString(h[:]))
	}

	s.respond(addr, msg.T, &krpc.Return{ID: string(s.nodeID[:])})
}

func (s *Server) handleSampleInfohashes(ctx context.Context, addr *net.UDPAddr, msg *krpc.Msg) {
	log := logger.FromContext(ctx).With("service", "dht", "handler", "sample_infohashes", "addr", addr.String())
	log.Debug("received request")

	if msg.A == nil {
		s.respondError(addr, msg.T, krpc.ErrProtocol, "missing arguments")
		return
	}
	target, err := table.ParseNodeID(msg.A.Target)
	if err != nil {
		s.respondError(addr, msg.T, krpc.ErrProtocol, "missing target")
		return
	}

	closest := s.table.Closest(target, s.bucketSize)
	s.respond(addr, msg.T, &krpc.Return{
		ID:    string(s.nodeID[:]),
		Nodes: table.EncodeNodes(closest),
	})
}

func (s *Server) respond(addr *net.UDPAddr, t string, r *krpc.Return) {
	select {
	case s.outbound <- &outMsg{addr: addr, msg: &krpc.Msg{T: t, Y: "r", R: r}}:
	default:
	}
}

func (s *Server) respondError(addr *net.UDPAddr, t string, code int, msg string) {
	select {
	case s.outbound <- &outMsg{addr: addr, msg: &krpc.Msg{T: t, Y: "e", E: []any{int64(code), msg}}}:
	default:
	}
}

func (s *Server) writeLoop(ctx context.Context) {
	for {
		select {
		case out := <-s.outbound:
			if err := s.rate.Wait(ctx); err != nil {
				return
			}
			data, err := bencode.Marshal(out.msg)
			if err != nil {
				continue
			}
			s.conn.WriteToUDP(data, out.addr) //nolint:errcheck
			s.rec.IncPacketsOutTotal()

		case <-ctx.Done():
			return
		}
	}
}

// insertNodesFromFindNode decodes a compact node list from a find_node response
// and inserts all nodes into the routing table.
func (s *Server) insertNodesFromFindNode(ctx context.Context, resp *krpc.Msg, from *net.UDPAddr, logKey string) {
	nodes, err := table.DecodeNodes(resp.R.Nodes)
	if err != nil {
		logger.FromContext(ctx).Debug(logKey+" failed to decode nodes",
			"service", "dht",
			"from", from.String(),
			"error", err,
		)
		return
	}
	for _, node := range nodes {
		result := s.table.Insert(ctx, node)
		s.rec.IncNodesDiscoveredTotal(result)
	}

	s.rec.SetRoutingTableSize(float64(s.table.NodeCount()))
	logger.FromContext(ctx).Debug(logKey+" nodes inserted",
		"service", "dht",
		"from", from.String(),
		"returned", len(nodes),
		"table_size", s.table.NodeCount(),
	)
}

func (s *Server) bucketRefreshLoop(ctx context.Context) {
	ticker := time.NewTicker(s.bucketRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			staleBuckets := s.table.StaleBuckets()
			if len(staleBuckets) == 0 {
				continue
			}
			queried := make(map[table.NodeID]bool)
			skipped := 0
			for _, b := range staleBuckets {
				target := b.GetRandomNodeID()
				for _, n := range s.table.Closest(target, s.bucketSize) {
					if queried[n.ID] {
						skipped++
						continue
					}
					queried[n.ID] = true
					go func() {
						resp, err := s.FindNode(ctx, n.Addr, n.ID, target)
						if err != nil || resp == nil || resp.R == nil {
							return
						}
						s.insertNodesFromFindNode(ctx, resp, n.Addr, "bucket refresh")
					}()
				}
			}
			logger.FromContext(ctx).Debug("bucket refresh tick",
				"service", "dht",
				"stale_buckets", len(staleBuckets),
				"queried", len(queried),
				"skipped_duplicates", skipped,
				"table_size", s.table.NodeCount(),
			)
		case <-ctx.Done():
			return
		}
	}
}

// Ping pings a node
func (s *Server) Ping(ctx context.Context, addr *net.UDPAddr, remoteID [20]byte) (*krpc.Msg, error) {
	return s.send(ctx, addr, remoteID, &krpc.Msg{
		Y: "q", Q: "ping",
		A: &krpc.MsgArgs{ID: string(s.nodeID[:])},
	})
}

// FindNode sends a BEP-05 find_node query.
func (s *Server) FindNode(ctx context.Context, addr *net.UDPAddr, remoteID table.NodeID, target table.NodeID) (*krpc.Msg, error) {
	return s.send(ctx, addr, remoteID, &krpc.Msg{
		Y: "q", Q: "find_node",
		A: &krpc.MsgArgs{
			ID:     string(s.nodeID[:]),
			Target: string(target[:]),
		},
	})
}

// GetPeers sends a BEP-05 get_peers query. Public — used by Crawler via DHT interface.
func (s *Server) GetPeers(ctx context.Context, addr *net.UDPAddr, remoteID table.NodeID, infoHash table.NodeID) (*krpc.Msg, error) {
	return s.send(ctx, addr, remoteID, &krpc.Msg{
		Y: "q", Q: "get_peers",
		A: &krpc.MsgArgs{
			ID:       string(s.nodeID[:]),
			InfoHash: string(infoHash[:]),
		},
	})
}

// SampleInfohashes sends a BEP-51 sample_infohashes query. Public — used by Crawler via DHT interface.
func (s *Server) SampleInfohashes(ctx context.Context, addr *net.UDPAddr, remoteID table.NodeID, target table.NodeID) (*krpc.Msg, error) {
	return s.send(ctx, addr, remoteID, &krpc.Msg{
		Y: "q", Q: "sample_infohashes",
		A: &krpc.MsgArgs{
			ID:     string(s.nodeID[:]),
			Target: string(target[:]),
		},
	})
}

func (s *Server) send(ctx context.Context, addr *net.UDPAddr, nodeID table.NodeID, msg *krpc.Msg) (*krpc.Msg, error) {
	txn := s.txns.New(nodeID, addr)
	msg.T = txn.ID

	select {
	case s.outbound <- &outMsg{addr: addr, msg: msg}:
	default:
		return nil, fmt.Errorf("outbound channel full")
	}

	select {
	case resp, ok := <-txn.Response:
		if !ok {
			return nil, fmt.Errorf("transaction timeout")
		}

		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
