package dht

import (
	"container/list"
	"context"
	"net"
	"sync"
	"time"

	pkgcache "github.com/kdwils/mgnx/pkg/cache"
)

type peerEntry struct {
	IP     net.IP
	Port   int
	SeenAt time.Time
}

// PeerStore is a bounded, TTL-aware store mapping infohash to peer entries.
// insertOrder is a FIFO list for eviction; orderIndex provides O(1) removal.
type PeerStore struct {
	c               *pkgcache.Cache[[20]byte, []peerEntry]
	mu              sync.Mutex
	insertOrder     *list.List
	orderIndex      map[[20]byte]*list.Element
	maxHashes       int
	maxPeersPerHash int
	ttl             time.Duration
}

func newPeerStore(maxHashes, maxPeersPerHash int, ttl time.Duration) *PeerStore {
	return &PeerStore{
		c:               pkgcache.New[[20]byte, []peerEntry](),
		insertOrder:     list.New(),
		orderIndex:      make(map[[20]byte]*list.Element),
		maxHashes:       maxHashes,
		maxPeersPerHash: maxPeersPerHash,
		ttl:             ttl,
	}
}

// Add records a peer for the given infohash. When a new infohash would exceed
// maxHashes, the oldest infohash is evicted first (FIFO).
func (ps *PeerStore) Add(ih [20]byte, ip net.IP, port int) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	existing, done := ps.getOrPrepare(ih, ip, port)
	if done {
		return
	}

	if len(existing) >= ps.maxPeersPerHash {
		return
	}

	updated := make([]peerEntry, len(existing)+1)
	copy(updated, existing)
	updated[len(existing)] = peerEntry{
		IP:     append(net.IP(nil), ip...),
		Port:   port,
		SeenAt: time.Now(),
	}
	ps.c.Set(ih, updated)
}

// getOrPrepare returns existing entries and whether to stop. Handles new infohash setup and duplicate refresh.
func (ps *PeerStore) getOrPrepare(ih [20]byte, ip net.IP, port int) ([]peerEntry, bool) {
	existing, exists := ps.c.Get(ih)

	if !exists {
		ps.prepareNewInfohash(ih)
		return nil, false
	}

	for i := range existing {
		if existing[i].IP.Equal(ip) && existing[i].Port == port {
			existing[i].SeenAt = time.Now()
			ps.c.Set(ih, existing)
			return existing, true
		}
	}

	return existing, false
}

func (ps *PeerStore) prepareNewInfohash(ih [20]byte) {
	if ps.c.Size() >= ps.maxHashes {
		ps.evictOldest()
	}
	el := ps.insertOrder.PushBack(ih)
	ps.orderIndex[ih] = el
}

// Get returns all live (non-expired) peers for ih, pruning expired entries in place.
func (ps *PeerStore) Get(ih [20]byte) []peerEntry {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	entries, ok := ps.c.Get(ih)
	if !ok {
		return nil
	}
	live := filterLivePeers(entries, ps.ttl)
	if len(live) == 0 {
		ps.c.Delete(ih)
		ps.removeFromOrder(ih)
		return nil
	}
	ps.c.Set(ih, live)
	return live
}

// startCleanup runs a periodic sweep that evicts fully-expired infohashes and
// prunes stale peer entries. Uses a consistent lock order (ps.mu → cache.mu)
// matching Add and Get, which avoids deadlock.
func (ps *PeerStore) startCleanup(ctx context.Context) {
	ticker := time.NewTicker(ps.ttl / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ps.pruneExpired()
		}
	}
}

func (ps *PeerStore) pruneExpired() {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	var next *list.Element
	for el := ps.insertOrder.Front(); el != nil; el = next {
		next = el.Next()
		ih := el.Value.([20]byte)
		entries, exists := ps.c.Get(ih)
		if !exists {
			ps.insertOrder.Remove(el)
			delete(ps.orderIndex, ih)
			continue
		}
		live := filterLivePeers(entries, ps.ttl)
		if len(live) == 0 {
			ps.c.Delete(ih)
			ps.insertOrder.Remove(el)
			delete(ps.orderIndex, ih)
			continue
		}
		if len(live) < len(entries) {
			ps.c.Set(ih, live)
		}
	}
}

// evictOldest removes the oldest live infohash. Skips ghost entries that were
// already evicted by a prior pruneExpired pass.
func (ps *PeerStore) evictOldest() {
	for ps.insertOrder.Len() > 0 {
		front := ps.insertOrder.Front()
		ih := front.Value.([20]byte)
		ps.insertOrder.Remove(front)
		delete(ps.orderIndex, ih)
		if _, exists := ps.c.Get(ih); exists {
			ps.c.Delete(ih)
			return
		}
	}
}

func (ps *PeerStore) removeFromOrder(ih [20]byte) {
	el, ok := ps.orderIndex[ih]
	if !ok {
		return
	}
	ps.insertOrder.Remove(el)
	delete(ps.orderIndex, ih)
}

// filterLivePeers returns a new slice containing only peers whose SeenAt is
// within ttl of now.
func filterLivePeers(entries []peerEntry, ttl time.Duration) []peerEntry {
	cutoff := time.Now().Add(-ttl)
	var live []peerEntry
	for _, e := range entries {
		if e.SeenAt.After(cutoff) {
			live = append(live, e)
		}
	}
	return live
}
