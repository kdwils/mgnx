package dht

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/logger"
	"golang.org/x/time/rate"
)

type outMsg struct {
	addr *net.UDPAddr
	msg  *Msg
}

type inMsg struct {
	addr *net.UDPAddr
	msg  *Msg
}

// Server is the UDP DHT node. It owns the socket, routing table, transaction
// manager, token manager, and the discovered output channel.
type Server struct {
	conn       *net.UDPConn
	ourID      NodeID
	table      *RoutingTable
	txns       *TxnManager
	outbound   chan *outMsg
	discovered chan DiscoveredPeers
	token      *TokenManager
	dedup      *BloomFilter
	rate       *rate.Limiter
	ipLimiter  *ipLimiter
	cfg        config.DHT
	handlers   chan inMsg
	bufPool    sync.Pool
	Resolver   Resolver
}

// NewServer binds the UDP socket and initialises all subsystems.
// Call Start to launch the goroutines.
func NewServer(cfg config.DHT) (*Server, error) {
	pc, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		return nil, err
	}
	conn := pc.(*net.UDPConn)
	setReceiveBuffer(conn)

	var ourID NodeID
	if cfg.NodeID != "" {
		ourID, err = ParseNodeIDHex(cfg.NodeID)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("invalid node_id: %w", err)
		}
	} else {
		rand.Read(ourID[:]) //nolint:errcheck — random bytes, no error possible
	}

	table := NewRoutingTable(ourID, cfg, nil)
	txns := NewTxnManager(table, cfg.TransactionTimeout)
	token := NewTokenManager(cfg.TokenRotation)

	s := &Server{
		conn:       conn,
		ourID:      ourID,
		table:      table,
		txns:       txns,
		outbound:   make(chan *outMsg, 512),
		discovered: make(chan DiscoveredPeers, cfg.DiscoveryBuffer),
		token:      token,
		rate:       rate.NewLimiter(rate.Limit(cfg.RateLimit), cfg.RateBurst),
		ipLimiter:  newIPLimiter(cfg.RateLimit, cfg.RateBurst, 5*time.Minute),
		cfg:        cfg,
		handlers:   make(chan inMsg, 512),
		bufPool: sync.Pool{
			New: func() any { return make([]byte, 2048) },
		},
		Resolver: net.DefaultResolver,
	}

	table.SetPinger(s)
	return s, nil
}

// Start loads the routing table from disk, then launches the read loop, write
// loop, query handler pool, and bucket refresh goroutine.
func (s *Server) Start(ctx context.Context) error {
	if err := s.table.Load(); err != nil {
		return err
	}
	s.txns.Start(ctx)
	s.token.Start(ctx)
	s.ipLimiter.Start(ctx)

	go s.readLoop(ctx)
	go s.writeLoop(ctx)
	for i := 0; i < s.cfg.Workers; i++ {
		go s.queryHandlerLoop(ctx)
	}
	go s.bucketRefreshLoop(ctx)

	logger.FromContext(ctx).Info("server started",
		"service", "dht",
		"addr", s.conn.LocalAddr(),
		"node_id", hex.EncodeToString(s.ourID[:]),
		"workers", s.cfg.Workers,
	)
	return nil
}

// Stop closes the UDP socket (causing the read loop to exit) and saves the
// routing table to disk.
func (s *Server) Stop(ctx context.Context) {
	s.conn.Close()
	s.table.Save(ctx) //nolint:errcheck — best-effort on shutdown
}

// Infohashes returns the channel of discovered infohash events.
func (s *Server) Infohashes() <-chan DiscoveredPeers {
	return s.discovered
}

// Addr returns the local UDP address the server is listening on.
func (s *Server) Addr() *net.UDPAddr {
	return s.conn.LocalAddr().(*net.UDPAddr)
}

// SetNodeID updates the server's node ID. Called once during bootstrap when the
// BEP-42 compliant ID is derived from the external IP. Must only be called
// before production traffic begins.
func (s *Server) SetNodeID(id NodeID) {
	s.ourID = id
	s.table.SetOurID(id)
}

// Query sends msg to addr and blocks until a response arrives or ctx expires.
// nodeID identifies the target node for failure tracking in the routing table.
func (s *Server) Query(ctx context.Context, addr *net.UDPAddr, nodeID NodeID, msg *Msg) (*Msg, error) {
	txn := s.txns.New(nodeID, addr)
	msg.T = txn.ID

	select {
	case s.outbound <- &outMsg{addr: addr, msg: msg}:
	default:
		return nil, fmt.Errorf("dht: outbound channel full")
	}

	select {
	case resp, ok := <-txn.Response:
		if !ok {
			return nil, fmt.Errorf("dht: query timed out")
		}
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// PingAsync implements Pinger. It dispatches a ping to node in a new goroutine
// and calls MarkSuccess on the routing table if the node responds.
// TxnManager automatically calls MarkFailure if the ping times out.
func (s *Server) PingAsync(ctx context.Context, node *Node) {
	go func() {
		pingCtx, cancel := context.WithTimeout(ctx, s.cfg.TransactionTimeout)
		defer cancel()
		resp, err := s.Query(pingCtx, node.Addr, node.ID, &Msg{
			Y: "q",
			Q: "ping",
			A: &MsgArgs{ID: string(s.ourID[:])},
		})
		if err != nil {
			return
		}
		if resp.R == nil {
			return
		}
		id, err := ParseNodeID(resp.R.ID)
		if err != nil {
			return
		}
		s.table.MarkSuccess(id)
	}()
}

// readLoop reads datagrams from the socket and routes them.
func (s *Server) readLoop(ctx context.Context) {
	for {
		buf := s.bufPool.Get().([]byte)
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			s.bufPool.Put(buf)
			if ctx.Err() != nil {
				return
			}
			return
		}

		// Copy before returning the pooled buffer; decoded structs must not
		// reference pool memory.
		data := make([]byte, n)
		copy(data, buf[:n])
		s.bufPool.Put(buf)

		var msg Msg
		if err := bencode.Unmarshal(data, &msg); err != nil {
			continue
		}
		s.routeMessage(addr, &msg)
	}
}

// routeMessage routes a decoded message to the transaction map (responses) or
// the query handler pool (queries).
func (s *Server) routeMessage(addr *net.UDPAddr, msg *Msg) {
	if msg.Y == "r" || msg.Y == "e" {
		s.txns.Complete(msg.T, msg)
		if msg.Y == "r" && msg.R != nil {
			s.updateTableFromResponse(addr, msg)
		}
		return
	}
	if msg.Y != "q" {
		return
	}
	select {
	case s.handlers <- inMsg{addr: addr, msg: msg}:
	default: // drop — handler pool is saturated
	}
}

// updateTableFromResponse inserts/refreshes the responding node in the table.
func (s *Server) updateTableFromResponse(addr *net.UDPAddr, msg *Msg) {
	id, err := ParseNodeID(msg.R.ID)
	if err != nil {
		return
	}
	if !isValidNodeID(addr.IP, id) {
		return
	}
	s.table.Insert(&Node{ID: id, Addr: addr, LastSeen: time.Now()})
	s.table.MarkSuccess(id)
}

// isValidNodeID reports whether id satisfies the BEP-42 security constraint
// for ip. Nodes that fail this check are not inserted into the routing table
// to prevent eclipse attacks via crafted node IDs.
// Non-IPv4 addresses always pass — BEP-42 is IPv4-only per project scope.
func isValidNodeID(ip net.IP, id NodeID) bool {
	if ip.To4() == nil {
		return true
	}
	return ValidateNodeIDForIP(ip, id) == nil
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

// processQuery dispatches a single inbound KRPC query (BEP-05 §KRPC Protocol)
// to the appropriate handler and adds the querying node to the routing table.
func (s *Server) processQuery(ctx context.Context, in inMsg) {
	log := logger.FromContext(ctx).With("service", "dht")

	if !s.ipLimiter.Allow(in.addr.IP) {
		return
	}

	log.Debug("processing request", "query", in.msg.Q, "from", in.addr)

	if in.msg.A != nil {
		if id, err := ParseNodeID(in.msg.A.ID); err == nil {
			if isValidNodeID(in.addr.IP, id) {
				s.table.Insert(&Node{ID: id, Addr: in.addr, LastSeen: time.Now()})
			}
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
		s.handleFindNode(ctx, in.addr, in.msg)
	}
}

// handlePing responds to a BEP-05 ping query (BEP-05 §ping).
func (s *Server) handlePing(ctx context.Context, addr *net.UDPAddr, msg *Msg) {
	log := logger.FromContext(ctx).With("service", "dht")
	log.Debug("handle_ping", "from", addr)
	s.respond(addr, msg.T, &Return{ID: string(s.ourID[:])})
}

// handleFindNode responds to a BEP-05 find_node query (BEP-05 §find node)
// by returning the k-closest nodes to the requested target.
func (s *Server) handleFindNode(ctx context.Context, addr *net.UDPAddr, msg *Msg) {
	log := logger.FromContext(ctx).With("service", "dht")
	log.Debug("handle_find_node", "from", addr)

	if msg.A == nil {
		s.respondError(addr, msg.T, ErrProtocol, "missing arguments")
		return
	}
	target, err := ParseNodeID(msg.A.Target)
	if err != nil {
		target, err = ParseNodeID(msg.A.InfoHash)
		if err != nil {
			s.respondError(addr, msg.T, ErrProtocol, "missing target")
			return
		}
	}
	closest := s.table.Closest(target, s.cfg.BucketSize)
	maxNodes := s.cfg.MaxNodesPerResponse
	if len(closest) > maxNodes {
		closest = closest[:maxNodes]
	}
	s.respond(addr, msg.T, &Return{
		ID:    string(s.ourID[:]),
		Nodes: EncodeNodes(closest),
	})
}

// handleGetPeers responds to a BEP-05 get_peers query (BEP-05 §get peers).
// Since this node is a crawler without a peer store, it always returns the
// k-closest nodes ("nodes" path) along with a token for future announce_peer.
// TODO: When peer storage is implemented, use MaxPeersPerResponse to bound Values.
func (s *Server) handleGetPeers(ctx context.Context, addr *net.UDPAddr, msg *Msg) {
	log := logger.FromContext(ctx).With("service", "dht", "handler", "get_peers", "addr", addr.String())
	log.Debug("received request")

	if msg.A == nil {
		log.Debug("missing arguments")
		s.respondError(addr, msg.T, ErrProtocol, "missing arguments")
		return
	}
	target, err := ParseNodeID(msg.A.InfoHash)
	if err != nil {
		log.Debug("missing info_hash")
		s.respondError(addr, msg.T, ErrProtocol, "missing info_hash")
		return
	}
	closest := s.table.Closest(target, s.cfg.BucketSize)
	maxNodes := s.cfg.MaxNodesPerResponse
	if len(closest) > maxNodes {
		closest = closest[:maxNodes]
	}
	s.respond(addr, msg.T, &Return{
		ID:    string(s.ourID[:]),
		Nodes: EncodeNodes(closest),
		Token: s.token.Generate(addr.IP),
	})
}

// handleAnnouncePeer handles a BEP-05 announce_peer query (BEP-05 §announce peer).
// The announcing node's address is emitted as a DiscoveredPeers event for the indexer.
func (s *Server) handleAnnouncePeer(ctx context.Context, addr *net.UDPAddr, msg *Msg) {
	log := logger.FromContext(ctx).With("service", "dht", "handler", "announce_peer", "addr", addr.String())
	log.Debug("received request")

	if msg.A == nil {
		log.Debug("missing arguments")
		s.respondError(addr, msg.T, ErrProtocol, "missing arguments")
		return
	}
	if !s.token.Validate(addr.IP, msg.A.Token) {
		log.Debug("invalid token")
		s.respondError(addr, msg.T, ErrProtocol, "bad token")
		return
	}
	var h [20]byte
	copy(h[:], msg.A.InfoHash)

	if s.dedup != nil && s.dedup.SeenOrAdd(h) {
		s.respond(addr, msg.T, &Return{ID: string(s.ourID[:])})
		return
	}

	port := msg.A.Port
	if msg.A.ImpliedPort != nil && *msg.A.ImpliedPort != 0 {
		port = addr.Port
	}
	if port <= 0 || port > 65535 {
		s.respond(addr, msg.T, &Return{ID: string(s.ourID[:])})
		return
	}
	event := DiscoveredPeers{
		Infohash: h,
		Peers:    []PeerAddr{{SourceIP: addr.IP, Port: port}},
		SeenAt:   time.Now(),
	}

	select {
	case s.discovered <- event:
		log.Debug("peer discovered", "infohash", hex.EncodeToString(h[:]), "from", addr)
	default:
		log.Debug("peer dropped due to full discovered channel", "infohash", hex.EncodeToString(h[:]))
	}

	s.respond(addr, msg.T, &Return{ID: string(s.ourID[:])})
}

// handleSampleInfohashes handles a BEP-51 sample_infohashes query (BEP-51 §3).
// As a crawler we hold no stored samples; we respond with closest nodes so the
// querier can continue its traversal.
func (s *Server) handleSampleInfohashes(ctx context.Context, addr *net.UDPAddr, msg *Msg) {
	log := logger.FromContext(ctx).With("service", "dht", "handler", "sample_infohashes", "addr", addr.String())
	log.Debug("received request")

	if msg.A == nil {
		s.respondError(addr, msg.T, ErrProtocol, "missing arguments")
		return
	}
	target, err := ParseNodeID(msg.A.Target)
	if err != nil {
		s.respondError(addr, msg.T, ErrProtocol, "missing target")
		return
	}

	// As a crawler we have no stored samples; respond with closest nodes.
	closest := s.table.Closest(target, s.cfg.BucketSize)
	s.respond(addr, msg.T, &Return{
		ID:    string(s.ourID[:]),
		Nodes: EncodeNodes(closest),
	})
}

// respond enqueues an outbound response. Drops silently if outbound is full.
func (s *Server) respond(addr *net.UDPAddr, t string, r *Return) {
	select {
	case s.outbound <- &outMsg{addr: addr, msg: &Msg{T: t, Y: "r", R: r}}:
	default:
	}
}

// respondError enqueues a BEP-05 KRPC error response.
// Format per BEP-05 §KRPC Protocol: {"y":"e","t":...,"e":[code, msg]}.
func (s *Server) respondError(addr *net.UDPAddr, t string, code int, msg string) {
	select {
	case s.outbound <- &outMsg{addr: addr, msg: &Msg{T: t, Y: "e", E: []any{int64(code), msg}}}:
	default:
	}
}

// writeLoop drains the outbound channel, rate-limits, encodes, and sends.
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
		case <-ctx.Done():
			return
		}
	}
}

// bucketRefreshLoop ticks every minute, finds stale buckets, and fires
// find_node queries to refresh them.
func (s *Server) bucketRefreshLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			for _, b := range s.table.StaleBuckets() {
				target := randomIDInBucket(b)
				for _, n := range s.table.Closest(target, s.cfg.BucketSize) {
					go func() {
						_, _ = s.Query(ctx, n.Addr, n.ID, &Msg{
							Y: "q",
							Q: "find_node",
							A: &MsgArgs{
								ID:     string(s.ourID[:]),
								Target: string(target[:]),
							},
						})
					}()
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// setReceiveBuffer sets SO_RCVBUF to 4 MB. The kernel silently clamps the
// value if the OS limit is lower — the socket remains functional.
func setReceiveBuffer(conn *net.UDPConn) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return
	}
	raw.Control(func(fd uintptr) {
		syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4<<20)
	})
}

// randomIDInBucket returns a random NodeID within the bucket's [Min, Max) range.
func randomIDInBucket(b *Bucket) NodeID {
	var id NodeID
	rand.Read(id[:])
	// Preserve the fixed prefix bits defined by b.Depth.
	byteIdx := b.Depth / 8
	bitIdx := 7 - (b.Depth % 8)
	for i := range byteIdx {
		id[i] = b.Min[i]
	}
	prefixMask := byte(0xFF) << (bitIdx + 1)
	id[byteIdx] = (b.Min[byteIdx] & prefixMask) | (id[byteIdx] &^ prefixMask)
	return id
}
