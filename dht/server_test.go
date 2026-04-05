package dht

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testServerCfg(t *testing.T) config.DHT {
	t.Helper()
	return config.DHT{
		Port:                0, // OS assigns a free port
		RateLimit:           1000,
		RateBurst:           1000,
		Workers:             2,
		DiscoveryBuffer:     100,
		TransactionTimeout:  2 * time.Second,
		TokenRotation:       5 * time.Minute,
		BucketSize:          8,
		GoodNodeWindow:      15 * time.Minute,
		BadFailureThreshold: 2,
		StaleThreshold:      15 * time.Minute,
		NodeIDPath:          t.TempDir() + "/dht_id",
		NodesPath:           t.TempDir() + "/dht_nodes.dat",
	}
}

func TestNewServer(t *testing.T) {
	t.Run("creates server with valid config", func(t *testing.T) {
		s, err := NewServer(testServerCfg(t))
		require.NoError(t, err)
		defer s.Stop()
		assert.NotNil(t, s.table)
		assert.NotNil(t, s.txns)
		assert.NotNil(t, s.token)
	})
}

func TestServer_processQuery(t *testing.T) {
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9999}

	makeServer := func(t *testing.T) *Server {
		t.Helper()
		s, err := NewServer(testServerCfg(t))
		require.NoError(t, err)
		t.Cleanup(s.Stop)
		return s
	}

	t.Run("ping returns our ID", func(t *testing.T) {
		s := makeServer(t)
		s.processQuery(context.Background(), inMsg{
			addr: addr,
			msg:  &Msg{T: "aa", Y: "q", Q: "ping", A: &MsgArgs{ID: string(make([]byte, 20))}},
		})
		select {
		case out := <-s.outbound:
			assert.Equal(t, "r", out.msg.Y)
			assert.Equal(t, string(s.ourID[:]), out.msg.R.ID)
		case <-time.After(time.Second):
			t.Fatal("no response enqueued")
		}
	})

	t.Run("find_node returns encoded nodes", func(t *testing.T) {
		s := makeServer(t)
		var target NodeID
		target[0] = 0x42
		s.processQuery(context.Background(), inMsg{
			addr: addr,
			msg: &Msg{
				T: "bb",
				Y: "q",
				Q: "find_node",
				A: &MsgArgs{ID: string(make([]byte, 20)), Target: string(target[:])},
			},
		})
		select {
		case out := <-s.outbound:
			assert.Equal(t, "r", out.msg.Y)
			assert.Equal(t, string(s.ourID[:]), out.msg.R.ID)
		case <-time.After(time.Second):
			t.Fatal("no response enqueued")
		}
	})

	t.Run("get_peers returns nodes and token", func(t *testing.T) {
		s := makeServer(t)
		var ih NodeID
		ih[0] = 0x11
		s.processQuery(context.Background(), inMsg{
			addr: addr,
			msg: &Msg{
				T: "cc",
				Y: "q",
				Q: "get_peers",
				A: &MsgArgs{ID: string(make([]byte, 20)), InfoHash: string(ih[:])},
			},
		})
		select {
		case out := <-s.outbound:
			assert.Equal(t, "r", out.msg.Y)
			assert.Len(t, out.msg.R.Token, 4)
		case <-time.After(time.Second):
			t.Fatal("no response enqueued")
		}
	})

	t.Run("announce_peer sends event to discovery channel", func(t *testing.T) {
		s := makeServer(t)
		var ih [20]byte
		ih[0] = 0xAB
		port := 12345
		s.processQuery(context.Background(), inMsg{
			addr: addr,
			msg: &Msg{
				T: "dd",
				Y: "q",
				Q: "announce_peer",
				A: &MsgArgs{
					ID:       string(make([]byte, 20)),
					InfoHash: string(ih[:]),
					Port:     port,
				},
			},
		})
		select {
		case event := <-s.discovered:
			assert.Equal(t, ih, event.Infohash)
			assert.Equal(t, port, event.Port)
		case <-time.After(time.Second):
			t.Fatal("no discovery event received")
		}
	})
}

func TestServer_ping_roundtrip(t *testing.T) {
	t.Run("two local servers complete a ping round-trip", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		s1, err := NewServer(testServerCfg(t))
		require.NoError(t, err)
		defer s1.Stop()
		require.NoError(t, s1.Start(ctx))

		s2, err := NewServer(testServerCfg(t))
		require.NoError(t, err)
		defer s2.Stop()
		require.NoError(t, s2.Start(ctx))

		s2Addr := &net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: s2.conn.LocalAddr().(*net.UDPAddr).Port,
		}
		resp, err := s1.Query(ctx, s2Addr, NodeID{}, &Msg{
			Y: "q",
			Q: "ping",
			A: &MsgArgs{ID: string(s1.ourID[:])},
		})
		require.NoError(t, err)
		assert.Equal(t, "r", resp.Y)
		assert.Equal(t, string(s2.ourID[:]), resp.R.ID)
	})
}
