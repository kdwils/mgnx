package dht

import (
	"container/list"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeerStore_Add(t *testing.T) {
	t.Run("stores peer for new infohash", func(t *testing.T) {
		ps := newPeerStore(100, 50, time.Minute)
		var ih [20]byte
		ih[0] = 0x01

		ps.Add(ih, net.ParseIP("1.2.3.4"), 6881)

		got := ps.Get(ih)
		require.Len(t, got, 1)
		assert.Equal(t, []peerEntry{{IP: net.ParseIP("1.2.3.4"), Port: 6881, SeenAt: got[0].SeenAt}}, got)
	})

	t.Run("appends multiple peers for the same infohash", func(t *testing.T) {
		ps := newPeerStore(100, 50, time.Minute)
		var ih [20]byte
		ih[0] = 0x02

		ps.Add(ih, net.ParseIP("1.2.3.4"), 6881)
		ps.Add(ih, net.ParseIP("5.6.7.8"), 6882)

		got := ps.Get(ih)
		require.Len(t, got, 2)
		assert.Equal(t, []peerEntry{
			{IP: net.ParseIP("1.2.3.4"), Port: 6881, SeenAt: got[0].SeenAt},
			{IP: net.ParseIP("5.6.7.8"), Port: 6882, SeenAt: got[1].SeenAt},
		}, got)
	})

	t.Run("evicts oldest infohash when store is at capacity", func(t *testing.T) {
		ps := newPeerStore(2, 50, time.Minute)
		var ih1, ih2, ih3 [20]byte
		ih1[0], ih2[0], ih3[0] = 0x01, 0x02, 0x03
		ip := net.ParseIP("1.2.3.4")

		ps.Add(ih1, ip, 6881)
		ps.Add(ih2, ip, 6882)
		ps.Add(ih3, ip, 6883)

		assert.Empty(t, ps.Get(ih1), "oldest infohash should be evicted")

		got2 := ps.Get(ih2)
		require.Len(t, got2, 1)
		assert.Equal(t, []peerEntry{{IP: ip, Port: 6882, SeenAt: got2[0].SeenAt}}, got2)

		got3 := ps.Get(ih3)
		require.Len(t, got3, 1)
		assert.Equal(t, []peerEntry{{IP: ip, Port: 6883, SeenAt: got3[0].SeenAt}}, got3)
	})

	t.Run("does not duplicate insert order for existing infohash", func(t *testing.T) {
		ps := newPeerStore(2, 50, time.Minute)
		var ih [20]byte
		ih[0] = 0x04

		ps.Add(ih, net.ParseIP("1.2.3.4"), 6881)
		ps.Add(ih, net.ParseIP("5.6.7.8"), 6882)

		assert.Equal(t, 1, ps.insertOrder.Len(), "same infohash should not appear in insertOrder twice")
	})

	t.Run("does not exceed maxPeersPerHash for a single infohash", func(t *testing.T) {
		ps := newPeerStore(100, 2, time.Minute)
		var ih [20]byte
		ih[0] = 0x05

		ps.Add(ih, net.ParseIP("1.2.3.4"), 6881)
		ps.Add(ih, net.ParseIP("5.6.7.8"), 6882)
		ps.Add(ih, net.ParseIP("9.10.11.12"), 6883)

		got := ps.Get(ih)
		require.Len(t, got, 2, "peer list should be capped at maxPeersPerHash")
	})

	t.Run("deduplicates same peer on re-announce", func(t *testing.T) {
		base := time.Now()
		refreshed := base.Add(10 * time.Millisecond)
		tick := base
		ps := &PeerStore{
			entries:         make(map[[20]byte][]peerEntry),
			insertOrder:     list.New(),
			orderIndex:      make(map[[20]byte]*list.Element),
			maxHashes:       100,
			maxPeersPerHash: 50,
			ttl:             time.Minute,
			now:             func() time.Time { return tick },
		}
		var ih [20]byte
		ih[0] = 0x06

		ip := net.ParseIP("1.2.3.4")
		ps.Add(ih, ip, 6881)
		tick = refreshed
		ps.Add(ih, ip, 6881)

		got := ps.Get(ih)
		assert.Equal(t, []peerEntry{{IP: ip, Port: 6881, SeenAt: refreshed}}, got)
	})
}

func TestPeerStore_Get(t *testing.T) {
	t.Run("returns nil for unknown infohash", func(t *testing.T) {
		ps := newPeerStore(100, 50, time.Minute)
		var ih [20]byte
		ih[0] = 0x01

		assert.Nil(t, ps.Get(ih))
	})

	t.Run("returns live peers", func(t *testing.T) {
		ps := newPeerStore(100, 50, time.Minute)
		var ih [20]byte
		ih[0] = 0x02

		ps.Add(ih, net.ParseIP("1.2.3.4"), 6881)

		got := ps.Get(ih)
		require.Len(t, got, 1)
		assert.Equal(t, []peerEntry{{IP: net.ParseIP("1.2.3.4"), Port: 6881, SeenAt: got[0].SeenAt}}, got)
	})

	t.Run("returns nil and removes infohash after TTL expires", func(t *testing.T) {
		var ih [20]byte
		ih[0] = 0x03
		ps := &PeerStore{
			entries: map[[20]byte][]peerEntry{
				ih: {{IP: net.ParseIP("1.2.3.4"), Port: 6881, SeenAt: time.Now().Add(-1 * time.Hour)}},
			},
			insertOrder:     list.New(),
			orderIndex:      make(map[[20]byte]*list.Element),
			maxHashes:       100,
			maxPeersPerHash: 50,
			ttl:             50 * time.Millisecond,
			now:             time.Now,
		}

		assert.Nil(t, ps.Get(ih))
		_, exists := ps.entries[ih]
		assert.False(t, exists, "expired infohash should be removed from store")
	})

	t.Run("filters expired peers but returns remaining live ones", func(t *testing.T) {
		var ih [20]byte
		ih[0] = 0x04
		liveIP := net.ParseIP("5.6.7.8")
		liveSeenAt := time.Now()
		ps := &PeerStore{
			entries: map[[20]byte][]peerEntry{
				ih: {
					{IP: net.ParseIP("1.2.3.4"), Port: 6881, SeenAt: time.Now().Add(-1 * time.Hour)},
					{IP: liveIP, Port: 6882, SeenAt: liveSeenAt},
				},
			},
			insertOrder:     list.New(),
			orderIndex:      make(map[[20]byte]*list.Element),
			maxHashes:       100,
			maxPeersPerHash: 50,
			ttl:             50 * time.Millisecond,
			now:             time.Now,
		}

		got := ps.Get(ih)
		assert.Equal(t, []peerEntry{{IP: liveIP, Port: 6882, SeenAt: liveSeenAt}}, got)
	})
}

func TestPeerStore_pruneExpired(t *testing.T) {
	t.Run("removes infohash when all peers have expired", func(t *testing.T) {
		var ih [20]byte
		ih[0] = 0x01
		ps := &PeerStore{
			entries: map[[20]byte][]peerEntry{
				ih: {{IP: net.ParseIP("1.2.3.4"), Port: 6881, SeenAt: time.Now().Add(-1 * time.Hour)}},
			},
			insertOrder:     list.New(),
			orderIndex:      make(map[[20]byte]*list.Element),
			maxHashes:       100,
			maxPeersPerHash: 50,
			ttl:             50 * time.Millisecond,
			now:             time.Now,
		}
		el := ps.insertOrder.PushBack(ih)
		ps.orderIndex[ih] = el

		ps.pruneExpired()

		_, exists := ps.entries[ih]
		assert.False(t, exists, "infohash should be removed after all peers expire")
	})

	t.Run("removes from insertOrder when infohash is evicted", func(t *testing.T) {
		var ih [20]byte
		ih[0] = 0x02
		ps := &PeerStore{
			entries: map[[20]byte][]peerEntry{
				ih: {{IP: net.ParseIP("1.2.3.4"), Port: 6881, SeenAt: time.Now().Add(-1 * time.Hour)}},
			},
			insertOrder:     list.New(),
			orderIndex:      make(map[[20]byte]*list.Element),
			maxHashes:       100,
			maxPeersPerHash: 50,
			ttl:             50 * time.Millisecond,
			now:             time.Now,
		}
		el := ps.insertOrder.PushBack(ih)
		ps.orderIndex[ih] = el

		ps.pruneExpired()

		for el := ps.insertOrder.Front(); el != nil; el = el.Next() {
			if el.Value.([20]byte) == ih {
				t.Fatal("infohash should be removed from insertOrder after eviction")
			}
		}
	})
}
