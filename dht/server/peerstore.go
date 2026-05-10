package server

import (
	"container/list"
	"context"
	"math/rand"
	"net"
	"sync"
	"time"
)

type peerEntry struct {
	IP     net.IP
	Port   int
	SeenAt time.Time
}

type SampleData struct {
	Samples string
	Num     int
	LastAt  time.Time
}

type PeerStore struct {
	mu              sync.Mutex
	entries         map[[20]byte][]peerEntry
	insertOrder     *list.List
	orderIndex      map[[20]byte]*list.Element
	maxHashes       int
	maxPeersPerHash int
	ttl             time.Duration
	now             func() time.Time
	cache           SampleData
}

func newPeerStore(maxHashes, maxPeersPerHash int, ttl time.Duration) *PeerStore {
	return &PeerStore{
		entries:         make(map[[20]byte][]peerEntry),
		insertOrder:     list.New(),
		orderIndex:      make(map[[20]byte]*list.Element),
		maxHashes:       maxHashes,
		maxPeersPerHash: maxPeersPerHash,
		ttl:             ttl,
		now:             time.Now,
	}
}

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

	ps.entries[ih] = append(existing, peerEntry{
		IP:     append(net.IP(nil), ip...),
		Port:   port,
		SeenAt: ps.now(),
	})
}

func (ps *PeerStore) getOrPrepare(ih [20]byte, ip net.IP, port int) ([]peerEntry, bool) {
	existing, exists := ps.entries[ih]

	if !exists {
		ps.prepareNewInfohash(ih)
		return nil, false
	}

	for i := range existing {
		if existing[i].IP.Equal(ip) && existing[i].Port == port {
			existing[i].SeenAt = ps.now()
			ps.entries[ih] = existing
			return existing, true
		}
	}

	return existing, false
}

func (ps *PeerStore) prepareNewInfohash(ih [20]byte) {
	if len(ps.entries) >= ps.maxHashes {
		ps.evictOldest()
	}
	el := ps.insertOrder.PushBack(ih)
	ps.orderIndex[ih] = el
}

func (ps *PeerStore) Get(ih [20]byte) []peerEntry {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	entries, ok := ps.entries[ih]
	if !ok {
		return nil
	}
	live := ps.filterLivePeers(entries)
	if len(live) == 0 {
		delete(ps.entries, ih)
		ps.removeFromOrder(ih)
		return nil
	}
	ps.entries[ih] = live
	return live
}

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
		entries, exists := ps.entries[ih]
		if !exists {
			ps.insertOrder.Remove(el)
			delete(ps.orderIndex, ih)
			continue
		}
		live := ps.filterLivePeers(entries)
		if len(live) == 0 {
			delete(ps.entries, ih)
			ps.insertOrder.Remove(el)
			delete(ps.orderIndex, ih)
			continue
		}
		if len(live) < len(entries) {
			ps.entries[ih] = live
		}
	}
}

func (ps *PeerStore) evictOldest() {
	for ps.insertOrder.Len() > 0 {
		front := ps.insertOrder.Front()
		ih := front.Value.([20]byte)
		ps.insertOrder.Remove(front)
		delete(ps.orderIndex, ih)
		if _, exists := ps.entries[ih]; exists {
			delete(ps.entries, ih)
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

func (ps *PeerStore) filterLivePeers(entries []peerEntry) []peerEntry {
	cutoff := ps.now().Add(-ps.ttl)
	var live []peerEntry
	for _, e := range entries {
		if e.SeenAt.After(cutoff) {
			live = append(live, e)
		}
	}
	return live
}

func (ps *PeerStore) Sample() (string, int) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	total := len(ps.entries)
	if total == 0 {
		return "", 0
	}

	if ps.now().Sub(ps.cache.LastAt) < 5*time.Minute && ps.cache.Samples != "" {
		return ps.cache.Samples, ps.cache.Num
	}

	keys := make([][20]byte, 0, total)
	for ih := range ps.entries {
		keys = append(keys, ih)
	}

	rand.Shuffle(len(keys), func(i, j int) {
		keys[i], keys[j] = keys[j], keys[i]
	})

	limit := 500
	if limit > total {
		limit = total
	}

	res := make([]byte, limit*20)
	for i := 0; i < limit; i++ {
		copy(res[i*20:], keys[i][:])
	}

	ps.cache = SampleData{
		Samples: string(res),
		Num:     total,
		LastAt:  ps.now(),
	}

	return ps.cache.Samples, ps.cache.Num
}
