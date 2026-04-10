package dht

import (
	"context"
	"encoding/hex"
	"net"
	"testing"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/recorder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bep42NodeID derives a BEP-42 compliant node ID for 127.0.0.1 and returns
// it as a hex string suitable for config.DHT.NodeID. Each call produces a
// fresh random ID (the BEP-42 middle bytes are random), so servers in the
// same test get distinct IDs while all passing validation.
func bep42NodeID(t *testing.T) string {
	t.Helper()
	id, err := DeriveNodeIDFromIP(net.ParseIP("127.0.0.1"))
	require.NoError(t, err)
	return hex.EncodeToString(id[:])
}

func testServerCfg(t *testing.T) config.DHT {
	t.Helper()
	return config.DHT{
		Port:                0,
		DiscoveryBuffer:     100,
		TransactionTimeout:  2 * time.Second,
		TokenRotation:       5 * time.Minute,
		BucketSize:          8,
		BadFailureThreshold: 2,
		StaleThreshold:      15 * time.Minute,
		NodeID:              bep42NodeID(t),
		NodesPath:           t.TempDir() + "/dht_nodes.dat",
		RateLimit:           1000,
		RateBurst:           1000,
		Workers:             2,
		MaxNodesPerResponse: 256,
		MaxPeersPerResponse: 50,
	}
}

func TestNewServer(t *testing.T) {
	t.Run("creates server with valid config", func(t *testing.T) {
		s, err := NewServer(testServerCfg(t), recorder.NewNoOp())
		require.NoError(t, err)
		defer s.Stop(t.Context())
		assert.NotNil(t, s.table)
		assert.NotNil(t, s.txns)
		assert.NotNil(t, s.token)
	})
}

func TestServer_processQuery(t *testing.T) {
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9999}

	makeServer := func(t *testing.T) *Server {
		t.Helper()
		s, err := NewServer(testServerCfg(t), recorder.NewNoOp())
		require.NoError(t, err)
		t.Cleanup(func() { s.Stop(t.Context()) })
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
		token := s.token.Generate(addr.IP)
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
					Token:    token,
				},
			},
		})
		select {
		case event := <-s.discovered:
			assert.Equal(t, ih, event.Infohash)
			assert.Equal(t, 1, len(event.Peers))
			assert.Equal(t, port, event.Peers[0].Port)
		case <-time.After(time.Second):
			t.Fatal("no discovery event received")
		}
	})
}

func TestServer_ping_roundtrip(t *testing.T) {
	t.Run("two local servers complete a ping round-trip", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		s1, err := NewServer(testServerCfg(t), recorder.NewNoOp())
		require.NoError(t, err)
		defer s1.Stop(t.Context())
		require.NoError(t, s1.Start(ctx))

		s2, err := NewServer(testServerCfg(t), recorder.NewNoOp())
		require.NoError(t, err)
		defer s2.Stop(t.Context())
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
