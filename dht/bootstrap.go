package dht

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"hash/crc32"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/kdwils/mgnx/logger"
)

const (
	bootstrapAlpha = 3
	bootstrapK     = 8
)

//go:generate go run go.uber.org/mock/mockgen -destination=mocks/mock_resolver.go -package=mocks github.com/kdwils/mgnx/dht Resolver
type Resolver interface {
	LookupHost(ctx context.Context, host string) (addrs []string, err error)
}

// Bootstrap performs the BEP-05 iterative find_node self-lookup convergence
// starting from addrs. It inserts discovered nodes into the routing table and
// triggers a bucket refresh for any stale buckets after convergence.
//
// If dht.node_id is configured, that ID is used directly (user-managed, must be
// BEP-42 compliant). Otherwise, the BEP-42 node ID is derived from the external IP
// returned by the first successful bootstrap response.
func (s *Server) Bootstrap(ctx context.Context, addrs []string) error {
	resolved := s.resolveBootstrapAddrs(ctx, addrs)
	log := logger.FromContext(ctx)
	log.Info("DHT bootstrap starting", "bootstrap_nodes", len(resolved))

	type entry struct {
		node    *Node
		dist    NodeID
		queried bool
	}

	shortlistMap := make(map[NodeID]*entry)

	addNodes := func(nodes []*Node) {
		for _, n := range nodes {
			if _, ok := shortlistMap[n.ID]; !ok {
				shortlistMap[n.ID] = &entry{
					node: n,
					dist: s.ourID.XOR(n.ID),
				}
			}
		}
	}

	sortedEntries := func() []*entry {
		entries := make([]*entry, 0, len(shortlistMap))
		for _, e := range shortlistMap {
			entries = append(entries, e)
		}
		sort.Slice(entries, func(i, j int) bool {
			return bytes.Compare(entries[i].dist[:], entries[j].dist[:]) < 0
		})
		return entries
	}

	type phase1Result struct {
		addr       *net.UDPAddr
		nodes      []*Node
		externalIP net.IP
		err        error
	}

	// Phase 1: query all bootstrap nodes concurrently.
	log.Info("querying bootstrap nodes concurrently", "count", len(resolved))
	results := make(chan phase1Result, len(resolved))
	var wg sync.WaitGroup
	for _, addr := range resolved {
		wg.Add(1)
		go func(addr *net.UDPAddr) {
			defer wg.Done()
			resp, err := s.Query(ctx, addr, NodeID{}, &Msg{
				Y: "q",
				Q: "find_node",
				A: &MsgArgs{
					ID:     string(s.ourID[:]),
					Target: string(s.ourID[:]),
				},
			})
			if err != nil {
				results <- phase1Result{addr: addr, err: err}
				return
			}
			r := phase1Result{addr: addr}
			if len(resp.IP) == 4 {
				r.externalIP = net.IP([]byte(resp.IP))
			}
			if resp.R != nil {
				r.nodes, _ = DecodeNodes(resp.R.Nodes)
			}
			results <- r
		}(addr)
	}
	wg.Wait()
	close(results)

	var externalIP net.IP
	contacted := 0
	for r := range results {
		if r.err != nil {
			log.Debug("bootstrap node unreachable", "addr", r.addr, "err", r.err)
			continue
		}
		contacted++
		if externalIP == nil && r.externalIP != nil {
			externalIP = r.externalIP
			log.Debug("external IP detected", "ip", externalIP)
		}
		for _, n := range r.nodes {
			s.table.Insert(n)
		}
		addNodes(r.nodes)
		log.Debug("bootstrap node responded", "addr", r.addr, "nodes_returned", len(r.nodes))
	}
	log.Info("DHT bootstrap phase 1 complete", "contacted", contacted, "shortlist", len(shortlistMap))

	// BEP-42: derive a compliant node ID from our external IP if not already set via config.
	// Skip if dht.node_id was provided (user-managed, already BEP-42 compliant).
	if s.cfg.NodeID == "" && externalIP != nil {
		if newID, err := DeriveBEP42NodeID(externalIP); err == nil {
			s.SetNodeID(newID)
			// Recompute distances with the new ID.
			for _, e := range shortlistMap {
				e.dist = s.ourID.XOR(e.node.ID)
			}
		}
	}

	// Phase 2: iterative find_node until convergence.
	// Stop when all bootstrapK closest nodes have been queried.
	round := 0
	for {
		entries := sortedEntries()

		limit := min(bootstrapK, len(entries))

		var toQuery []*entry
		for _, e := range entries[:limit] {
			if !e.queried {
				toQuery = append(toQuery, e)
				if len(toQuery) == bootstrapAlpha {
					break
				}
			}
		}

		if len(toQuery) == 0 {
			break
		}

		round++
		for _, e := range toQuery {
			e.queried = true
			resp, err := s.Query(ctx, e.node.Addr, e.node.ID, &Msg{
				Y: "q",
				Q: "find_node",
				A: &MsgArgs{
					ID:     string(s.ourID[:]),
					Target: string(s.ourID[:]),
				},
			})
			if err != nil {
				log.Debug("iterative find_node failed", "addr", e.node.Addr, "err", err)
				continue
			}
			if resp.R == nil {
				continue
			}
			nodes, err := DecodeNodes(resp.R.Nodes)
			if err != nil {
				continue
			}
			for _, n := range nodes {
				s.table.Insert(n)
			}
			addNodes(nodes)
		}
		log.Debug("bootstrap convergence round", "round", round, "shortlist", len(shortlistMap), "table_nodes", s.table.NodeCount())
	}

	log.Info("DHT bootstrap complete", "table_nodes", s.table.NodeCount())

	// Phase 3: refresh any stale buckets after convergence.
	for _, b := range s.table.StaleBuckets() {
		target := randomIDInBucket(b)
		for _, n := range s.table.Closest(target, s.cfg.BucketSize) {
			go func() {
				qCtx, cancel := context.WithTimeout(ctx, s.cfg.TransactionTimeout)
				defer cancel()
				_, _ = s.Query(qCtx, n.Addr, n.ID, &Msg{
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

	return nil
}

// resolveBootstrapAddrs resolves "host:port" strings to UDP addresses,
// accepting multiple A records per hostname.
func (s *Server) resolveBootstrapAddrs(ctx context.Context, addrs []string) []*net.UDPAddr {
	var result []*net.UDPAddr
	for _, addr := range addrs {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		ips, err := s.Resolver.LookupHost(ctx, host)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			udpAddr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(ip, port))
			if err != nil {
				continue
			}
			result = append(result, udpAddr)
		}
	}
	return result
}

// DeriveBEP42NodeID generates a BEP-42 compliant node ID for the given IPv4
// address. See https://www.bittorrent.org/beps/bep_0042.html
//
// Derivation (IPv4):
//
//	r          = random 0–7
//	masked_ip  = ip_uint32 & 0x030f3fff
//	seed       = masked_ip | (r << 29)   (4 bytes, big-endian)
//	crc        = crc32c(seed)
//	id[0..1]   = top 16 bits of crc
//	id[2]      = (crc >> 8) & 0xf8 | random_3_bits
//	id[3..18]  = random
//	id[19]     = random_5_bits | r
func DeriveBEP42NodeID(ip net.IP) (NodeID, error) {
	ip4 := ip.To4()
	if ip4 == nil {
		return NodeID{}, nil // IPv6 out of scope per project charter
	}

	var rBuf [1]byte
	rand.Read(rBuf[:]) //nolint:errcheck
	r := rBuf[0] & 0x07

	ipUint32 := binary.BigEndian.Uint32(ip4)
	seed := (ipUint32 & 0x030f3fff) | (uint32(r) << 29)

	var seedBuf [4]byte
	binary.BigEndian.PutUint32(seedBuf[:], seed)

	table := crc32.MakeTable(crc32.Castagnoli)
	crc := crc32.Checksum(seedBuf[:], table)

	var id NodeID
	rand.Read(id[:]) //nolint:errcheck

	// Top 21 bits of id must equal top 21 bits of crc.
	id[0] = byte(crc >> 24)
	id[1] = byte(crc >> 16)
	id[2] = (byte(crc>>8) & 0xf8) | (id[2] & 0x07)
	// id[3..18] already random
	id[19] = (id[19] & 0xf8) | r

	return id, nil
}

// saveNodeID writes id to path atomically (temp file + rename).
func saveNodeID(path string, id NodeID) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, id[:], 0o600); err != nil {
		return
	}
	os.Rename(tmp, path) //nolint:errcheck
}
