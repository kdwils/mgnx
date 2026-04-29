package dht

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeerStore_Add(t *testing.T) {
	t.Run("stores peer for new infohash", func(t *testing.T) {
		ps := newPeerStore(100, time.Minute)
		var ih [20]byte
		ih[0] = 0x01

		ps.Add(ih, net.ParseIP("1.2.3.4"), 6881)

		got := ps.Get(ih)
		require.Len(t, got, 1)
		want := []peerEntry{{IP: net.ParseIP("1.2.3.4"), Port: 6881, SeenAt: got[0].SeenAt}}
		assert.Equal(t, want, got)
	})

	t.Run("appends multiple peers for the same infohash", func(t *testing.T) {
		ps := newPeerStore(100, time.Minute)
		var ih [20]byte
		ih[0] = 0x02

		ps.Add(ih, net.ParseIP("1.2.3.4"), 6881)
		ps.Add(ih, net.ParseIP("5.6.7.8"), 6882)

		got := ps.Get(ih)
		require.Len(t, got, 2)
		want := []peerEntry{
			{IP: net.ParseIP("1.2.3.4"), Port: 6881, SeenAt: got[0].SeenAt},
			{IP: net.ParseIP("5.6.7.8"), Port: 6882, SeenAt: got[1].SeenAt},
		}
		assert.Equal(t, want, got)
	})

	t.Run("evicts oldest infohash when store is at capacity", func(t *testing.T) {
		ps := newPeerStore(2, time.Minute)
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
		ps := newPeerStore(2, time.Minute)
		var ih [20]byte
		ih[0] = 0x04

		ps.Add(ih, net.ParseIP("1.2.3.4"), 6881)
		ps.Add(ih, net.ParseIP("5.6.7.8"), 6882)

		ps.mu.Lock()
		orderLen := len(ps.insertOrder)
		ps.mu.Unlock()
		assert.Equal(t, 1, orderLen, "same infohash should not appear in insertOrder twice")
	})
}

func TestPeerStore_Get(t *testing.T) {
	t.Run("returns nil for unknown infohash", func(t *testing.T) {
		ps := newPeerStore(100, time.Minute)
		var ih [20]byte
		ih[0] = 0x01

		assert.Nil(t, ps.Get(ih))
	})

	t.Run("returns live peers", func(t *testing.T) {
		ps := newPeerStore(100, time.Minute)
		var ih [20]byte
		ih[0] = 0x02

		ps.Add(ih, net.ParseIP("1.2.3.4"), 6881)

		got := ps.Get(ih)
		require.Len(t, got, 1)
		want := []peerEntry{{IP: net.ParseIP("1.2.3.4"), Port: 6881, SeenAt: got[0].SeenAt}}
		assert.Equal(t, want, got)
	})

	t.Run("returns nil and removes infohash after TTL expires", func(t *testing.T) {
		ps := newPeerStore(100, 50*time.Millisecond)
		var ih [20]byte
		ih[0] = 0x03

		ps.Add(ih, net.ParseIP("1.2.3.4"), 6881)
		time.Sleep(100 * time.Millisecond)

		assert.Nil(t, ps.Get(ih))

		_, exists := ps.c.Get(ih)
		assert.False(t, exists, "expired infohash should be removed from store")
	})

	t.Run("filters expired peers but returns remaining live ones", func(t *testing.T) {
		ps := newPeerStore(100, 80*time.Millisecond)
		var ih [20]byte
		ih[0] = 0x04

		ps.Add(ih, net.ParseIP("1.2.3.4"), 6881)
		time.Sleep(100 * time.Millisecond) // first peer expires
		ps.Add(ih, net.ParseIP("5.6.7.8"), 6882)

		got := ps.Get(ih)
		require.Len(t, got, 1, "only the live peer should be returned")
		want := []peerEntry{{IP: net.ParseIP("5.6.7.8"), Port: 6882, SeenAt: got[0].SeenAt}}
		assert.Equal(t, want, got)
	})
}

func TestPeerStore_startCleanup(t *testing.T) {
	t.Run("removes infohash when all peers have expired", func(t *testing.T) {
		ps := newPeerStore(100, 50*time.Millisecond)
		var ih [20]byte
		ih[0] = 0x01

		ps.Add(ih, net.ParseIP("1.2.3.4"), 6881)

		go ps.startCleanup(t.Context())
		time.Sleep(200 * time.Millisecond)

		_, exists := ps.c.Get(ih)
		assert.False(t, exists, "infohash should be removed after all peers expire")
	})
}
