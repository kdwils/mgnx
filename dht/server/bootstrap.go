package server

import (
	"bytes"
	"context"
	"net"
	"sort"

	"github.com/kdwils/mgnx/dht/table"
	"github.com/kdwils/mgnx/logger"
	"github.com/kdwils/mgnx/pkg/cache"
	"golang.org/x/sync/errgroup"
)

const (
	bootstrapAlpha = 3
	bootstrapK     = 8
)

//go:generate go run go.uber.org/mock/mockgen -destination=mocks/resolver/mock_resolver.go -package=mocks github.com/kdwils/mgnx/dht Resolver
type Resolver interface {
	LookupHost(ctx context.Context, host string) (addrs []string, err error)
}

type bootstrapNodes struct {
	c *cache.Cache[table.NodeID, *entry]
}

type entry struct {
	node    *table.Node
	dist    table.NodeID
	queried bool
}

func newBootstrapNodes() *bootstrapNodes {
	return &bootstrapNodes{c: cache.New[table.NodeID, *entry]()}
}

func (bn *bootstrapNodes) add(nodes []*table.Node, serverNodeID table.NodeID) {
	for _, n := range nodes {
		if _, ok := bn.c.Get(n.ID); !ok {
			bn.c.Set(n.ID, &entry{
				node: n,
				dist: serverNodeID.XOR(n.ID),
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

func (bn *bootstrapNodes) len() int {
	return bn.c.Size()
}

func (s *Server) Bootstrap(ctx context.Context, addrs []string) error {
	log := logger.FromContext(ctx).With("service", "dht")

	resolved := s.resolveBootstrapAddrs(ctx, addrs)
	log.Info("bootstrap starting", "bootstrap_nodes", len(resolved))

	bn := newBootstrapNodes()
	err := s.querySeeds(ctx, resolved, bn)
	if err != nil {
		return err
	}

	if err := s.convergeTable(ctx, bn); err != nil {
		return err
	}

	s.refreshStaleBuckets(ctx)
	return nil
}

type seedResult struct {
	addr       *net.UDPAddr
	nodes      []*table.Node
	externalIP net.IP
	err        error
}

func (s *Server) querySeeds(ctx context.Context, addrs []*net.UDPAddr, bn *bootstrapNodes) error {
	log := logger.FromContext(ctx).With("service", "dht")

	eg, egCtx := errgroup.WithContext(ctx)
	results := make(chan seedResult, len(addrs))

	for _, addr := range addrs {
		eg.Go(func() error {
			select {
			case <-egCtx.Done():
				return egCtx.Err()
			default:
			}

			resp, err := s.FindNode(ctx, addr, table.NodeID{}, s.nodeID)
			if err != nil {
				results <- seedResult{addr: addr, err: err}
				return nil
			}
			r := seedResult{addr: addr}
			if len(resp.IP) == 4 {
				r.externalIP = net.IP([]byte(resp.IP))
			}
			if resp.R != nil {
				r.nodes, _ = table.DecodeNodes(resp.R.Nodes)
			}
			results <- r
			return nil
		})
	}

	go func() {
		eg.Wait()
		close(results)
	}()

	contacted := 0
	for r := range results {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if r.err != nil {
			log.Debug("bootstrap node unreachable", "addr", r.addr.String(), "err", r.err)
			continue
		}
		contacted++
		for _, n := range r.nodes {
			if s.table.InsertValidNode(ctx, n) == table.NodeInsertDropped {
				log.Debug("bootstrap node rejected with invalid ID for IP",
					"service", "dht",
					"node_addr", n.Addr.String(),
				)
			}
		}
		bn.add(r.nodes, s.nodeID)
		log.Debug("bootstrap node responded", "addr", r.addr.String(), "nodes_returned", len(r.nodes))
	}

	log.Info("bootstrap phase 1 complete", "contacted", contacted, "shortlist", bn.len())

	return nil
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

		g, gctx := errgroup.WithContext(ctx)
		for _, e := range toQuery {
			g.Go(func() error {
				resp, err := s.FindNode(gctx, e.node.Addr, e.node.ID, s.nodeID)
				e.queried = true
				if err != nil {
					log.Debug("iterative find_node failed", "addr", e.node.Addr.String(), "err", err)
					return nil
				}
				if resp.R == nil {
					return nil
				}
				nodes, err := table.DecodeNodes(resp.R.Nodes)
				if err != nil {
					return nil
				}

				for _, n := range nodes {
					if s.table.InsertValidNode(gctx, n) == table.NodeInsertDropped {
						log.Debug("bootstrap convergence rejected node with invalid ID for IP",
							"service", "dht",
							"node_addr", n.Addr.String(),
						)
					}
				}

				bn.add(nodes, s.nodeID)
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return err
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
		target := b.GetRandomNodeID()
		for _, n := range s.table.Closest(target, s.bucketSize) {
			go func() {
				qCtx, cancel := context.WithTimeout(ctx, s.transactionTimeout)
				defer cancel()
				resp, err := s.FindNode(qCtx, n.Addr, n.ID, target)

				if err != nil || resp == nil || resp.R == nil {
					return
				}
				s.insertNodesFromFindNode(ctx, resp, n.Addr, "stale bucket refresh")
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
