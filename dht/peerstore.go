package dht

import (
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

// PeerStore is a bounded, TTL-aware store mapping infohash to peer entries,
// backed by pkg/cache. insertOrder tracks insertion sequence for FIFO eviction.
type PeerStore struct {
	c               *pkgcache.Cache[[20]byte, []peerEntry]
	mu              sync.Mutex
	insertOrder     [][20]byte
	maxHashes       int
	maxPeersPerHash int
	ttl             time.Duration
}

func newPeerStore(maxHashes, maxPeersPerHash int, ttl time.Duration) *PeerStore {
	return &PeerStore{
		c: pkgcache.New[[20]byte, []peerEntry](
			pkgcache.WithCleanupInterval[[20]byte, []peerEntry](ttl / 2),
		),
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

	existing, exists := ps.c.Get(ih)
	if !exists {
		if ps.c.Size() >= ps.maxHashes {
			ps.evictOldest()
		}
		ps.insertOrder = append(ps.insertOrder, ih)
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

// Get returns all live (non-expired) peers for ih, pruning expired entries in place.
func (ps *PeerStore) Get(ih [20]byte) []peerEntry {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	entries, ok := ps.c.Get(ih)
	if !ok {
		return nil
	}
	snapshot := make([]peerEntry, len(entries))
	copy(snapshot, entries)
	live := filterLivePeers(snapshot, ps.ttl)
	if len(live) == 0 {
		ps.c.Delete(ih)
		ps.removeFromOrder(ih)
		return nil
	}
	ps.c.Set(ih, live)
	return live
}

// startCleanup blocks, delegating periodic eviction of fully-expired infohashes
// to the cache's Cleanup method. Intended to be launched via go.
func (ps *PeerStore) startCleanup(ctx context.Context) {
	ps.c.Cleanup(ctx, func(_ [20]byte, entries []peerEntry) bool {
		snapshot := make([]peerEntry, len(entries))
		copy(snapshot, entries)
		return len(filterLivePeers(snapshot, ps.ttl)) == 0
	})
}

// evictOldest removes the oldest live infohash. Skips ghost entries that were
// already evicted by background TTL cleanup, keeping insertOrder in sync.
func (ps *PeerStore) evictOldest() {
	for len(ps.insertOrder) > 0 {
		oldest := ps.insertOrder[0]
		ps.insertOrder = ps.insertOrder[1:]
		if _, exists := ps.c.Get(oldest); exists {
			ps.c.Delete(oldest)
			return
		}
	}
}

func (ps *PeerStore) removeFromOrder(ih [20]byte) {
	for i, id := range ps.insertOrder {
		if id != ih {
			continue
		}
		ps.insertOrder = append(ps.insertOrder[:i], ps.insertOrder[i+1:]...)
		return
	}
}

func filterLivePeers(entries []peerEntry, ttl time.Duration) []peerEntry {
	cutoff := time.Now().Add(-ttl)
	live := entries[:0] // reuses backing array; callers must pass a copy
	for _, e := range entries {
		if e.SeenAt.After(cutoff) {
			live = append(live, e)
		}
	}
	return live
}
