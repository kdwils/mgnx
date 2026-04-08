package dht

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"net"
	"os"
	"path/filepath"
	"sort"

	"github.com/kdwils/mgnx/logger"
	"github.com/kdwils/mgnx/pkg/cache"
	"golang.org/x/sync/errgroup"
)

const (
	bootstrapAlpha = 3
	bootstrapK     = 8
)

//go:generate go run go.uber.org/mock/mockgen -destination=mocks/mock_resolver.go -package=mocks github.com/kdwils/mgnx/dht Resolver
type Resolver interface {
	LookupHost(ctx context.Context, host string) (addrs []string, err error)
}

type bootstrapNodes struct {
	c *cache.Cache[NodeID, *entry]
}

type entry struct {
	node    *Node
	dist    NodeID
	queried bool
}

func newBootstrapNodes() *bootstrapNodes {
	return &bootstrapNodes{c: cache.New[NodeID, *entry]()}
}

func (bn *bootstrapNodes) add(nodes []*Node, ourID NodeID) {
	for _, n := range nodes {
		if _, ok := bn.c.Get(n.ID); !ok {
			bn.c.Set(n.ID, &entry{
				node: n,
				dist: ourID.XOR(n.ID),
			})
		}
	}
}

func (bn *bootstrapNodes) closestUnqueried(k, alpha int) []*entry {
	entries := make([]*entry, 0, bn.c.Size())
	for _, e := range bn.c.Items() {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].dist[:], entries[j].dist[:]) < 0
	})

	limit := min(k, len(entries))
	var result []*entry
	for _, e := range entries[:limit] {
		if !e.queried {
			result = append(result, e)
			if len(result) == alpha {
				break
			}
		}
	}
	return result
}

func (bn *bootstrapNodes) recomputeDistances(ourID NodeID) {
	for _, e := range bn.c.Items() {
		e.dist = ourID.XOR(e.node.ID)
	}
}

func (bn *bootstrapNodes) len() int {
	return bn.c.Size()
}

func (bn *bootstrapNodes) all() []*entry {
	entries := make([]*entry, 0, bn.c.Size())
	for _, e := range bn.c.Items() {
		entries = append(entries, e)
	}
	return entries
}

func (s *Server) Bootstrap(ctx context.Context, addrs []string) error {
	log := logger.FromContext(ctx).With("service", "dht")

	resolved := s.resolveBootstrapAddrs(ctx, addrs)
	log.Info("bootstrap starting", "bootstrap_nodes", len(resolved))

	bn := newBootstrapNodes()
	externalIP, err := s.querySeeds(ctx, resolved, bn)
	if err != nil {
		return err
	}

	if s.cfg.NodeID == "" && externalIP != nil {
		if newID, err := DeriveNodeIDFromIP(externalIP); err == nil {
			s.SetNodeID(newID)
			bn.recomputeDistances(s.ourID)
		}
	}

	if err := s.convergeTable(ctx, bn); err != nil {
		return err
	}

	s.refreshStaleBuckets(ctx)
	return nil
}

func (s *Server) querySeeds(ctx context.Context, addrs []*net.UDPAddr, bn *bootstrapNodes) (net.IP, error) {
	log := logger.FromContext(ctx).With("service", "dht")

	type seedResult struct {
		addr       *net.UDPAddr
		nodes      []*Node
		externalIP net.IP
		err        error
	}

	eg, egCtx := errgroup.WithContext(ctx)
	results := make(chan seedResult, len(addrs))

	for _, addr := range addrs {
		eg.Go(func() error {
			select {
			case <-egCtx.Done():
				return egCtx.Err()
			default:
			}
			resp, err := s.Query(egCtx, addr, NodeID{}, &Msg{
				Y: "q",
				Q: "find_node",
				A: &MsgArgs{
					ID:     string(s.ourID[:]),
					Target: string(s.ourID[:]),
				},
			})
			if err != nil {
				results <- seedResult{addr: addr, err: err}
				return nil
			}
			r := seedResult{addr: addr}
			if len(resp.IP) == 4 {
				r.externalIP = net.IP([]byte(resp.IP))
			}
			if resp.R != nil {
				r.nodes, _ = DecodeNodes(resp.R.Nodes)
			}
			results <- r
			return nil
		})
	}

	go func() {
		eg.Wait()
		close(results)
	}()

	var externalIP net.IP
	contacted := 0
	for r := range results {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
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
		bn.add(r.nodes, s.ourID)
		log.Debug("bootstrap node responded", "addr", r.addr, "nodes_returned", len(r.nodes))
	}

	log.Info("bootstrap phase 1 complete", "contacted", contacted, "shortlist", bn.len())

	return externalIP, nil
}

func (s *Server) convergeTable(ctx context.Context, bn *bootstrapNodes) error {
	log := logger.FromContext(ctx).With("service", "dht")

	round := 0
	for {
		toQuery := bn.closestUnqueried(bootstrapK, bootstrapAlpha)
		if len(toQuery) == 0 {
			break
		}

		round++

		for _, e := range toQuery {
			resp, err := s.Query(ctx, e.node.Addr, e.node.ID, &Msg{
				Y: "q",
				Q: "find_node",
				A: &MsgArgs{
					ID:     string(s.ourID[:]),
					Target: string(s.ourID[:]),
				},
			})
			e.queried = true
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
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
			bn.add(nodes, s.ourID)
		}
		log.Debug("bootstrap convergence round", "round", round, "shortlist", bn.len(), "table_nodes", s.table.NodeCount())
	}

	log.Info("bootstrap complete", "table_nodes", s.table.NodeCount())
	return nil
}

func (s *Server) refreshStaleBuckets(ctx context.Context) {
	for _, b := range s.table.StaleBuckets() {
		if ctx.Err() != nil {
			return
		}
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
}

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

// DeriveNodeIDFromIP generates a BEP-42 compliant node ID for the given IPv4
// address. Implements BEP-42 (DHT Security Extension) §Node ID.
// See https://www.bittorrent.org/beps/bep_0042.html
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
func DeriveNodeIDFromIP(ip net.IP) (NodeID, error) {
	ip4 := ip.To4()
	if ip4 == nil {
		return NodeID{}, nil
	}

	var rBuf [1]byte
	rand.Read(rBuf[:])
	r := rBuf[0] & 0x07

	ipUint32 := binary.BigEndian.Uint32(ip4)
	seed := (ipUint32 & 0x030f3fff) | (uint32(r) << 29)

	var seedBuf [4]byte
	binary.BigEndian.PutUint32(seedBuf[:], seed)

	table := crc32.MakeTable(crc32.Castagnoli)
	crc := crc32.Checksum(seedBuf[:], table)

	var id NodeID
	rand.Read(id[:])

	id[0] = byte(crc >> 24)
	id[1] = byte(crc >> 16)
	id[2] = (byte(crc>>8) & 0xf8) | (id[2] & 0x07)
	id[19] = (id[19] & 0xf8) | r

	return id, nil
}

func ValidateNodeIDForIP(ip net.IP, id NodeID) error {
	ip4 := ip.To4()
	if ip4 == nil {
		return errors.New("not an IPv4 address")
	}

	r := id[19] & 0x07
	seed := (binary.BigEndian.Uint32(ip4) & 0x030f3fff) | (uint32(r) << 29)
	var seedBuf [4]byte
	binary.BigEndian.PutUint32(seedBuf[:], seed)
	crc := crc32.Checksum(seedBuf[:], crc32.MakeTable(crc32.Castagnoli))

	if byte(crc>>24) != id[0] {
		return fmt.Errorf("id[0] (%02x) does not match crc[0] (%02x)", id[0], byte(crc>>24))
	}
	if byte(crc>>16) != id[1] {
		return fmt.Errorf("id[1] (%02x) does not match crc[1] (%02x)", id[1], byte(crc>>16))
	}
	if byte(crc>>8)&0xf8 != id[2]&0xf8 {
		return fmt.Errorf("id[2] top 5 bits (%02x) do not match crc bits (%02x)", id[2]&0xf8, byte(crc>>8)&0xf8)
	}
	return nil
}

func saveNodeID(path string, id NodeID) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, id[:], 0o600); err != nil {
		return
	}
	os.Rename(tmp, path)
}
