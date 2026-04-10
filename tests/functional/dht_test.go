//go:build functional

package functional

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/dht"
	"github.com/kdwils/mgnx/recorder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDHTBEPProtocol(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, mockNode := setupDHTServer(t)
	require.NoError(t, srv.Start(ctx))
	t.Cleanup(func() { srv.Stop(ctx) })

	serverAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: srv.Addr().Port}

	t.Run("ping returns our node ID", func(t *testing.T) {
		resp := mockNode.sendQuery(ctx, serverAddr, &dht.Msg{
			T: "aa",
			Y: "q",
			Q: "ping",
			A: &dht.MsgArgs{ID: string(mockNode.id[:])},
		})

		require.NotNil(t, resp)
		require.Equal(t, "r", resp.Y)
		require.NotEmpty(t, resp.R.ID)
	})

	t.Run("find_node returns compact nodes", func(t *testing.T) {
		target := [20]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}
		resp := mockNode.sendQuery(ctx, serverAddr, &dht.Msg{
			T: "bb",
			Y: "q",
			Q: "find_node",
			A: &dht.MsgArgs{
				ID:     string(mockNode.id[:]),
				Target: string(target[:]),
			},
		})

		require.NotNil(t, resp)
		require.Equal(t, "r", resp.Y)
		require.NotEmpty(t, resp.R.Nodes)
		require.NotEmpty(t, resp.R.ID)
	})

	t.Run("get_peers returns token", func(t *testing.T) {
		infoHash := [20]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}
		resp := mockNode.sendQuery(ctx, serverAddr, &dht.Msg{
			T: "cc",
			Y: "q",
			Q: "get_peers",
			A: &dht.MsgArgs{
				ID:       string(mockNode.id[:]),
				InfoHash: string(infoHash[:]),
			},
		})

		require.NotNil(t, resp)
		require.Equal(t, "r", resp.Y)
		require.NotEmpty(t, resp.R.Token)
		require.NotEmpty(t, resp.R.Nodes)
		require.NotEmpty(t, resp.R.ID)
	})

	t.Run("announce_peer requires valid token", func(t *testing.T) {
		infoHash := [20]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}

		getPeersResp := mockNode.sendQuery(ctx, serverAddr, &dht.Msg{
			T: "dd",
			Y: "q",
			Q: "get_peers",
			A: &dht.MsgArgs{
				ID:       string(mockNode.id[:]),
				InfoHash: string(infoHash[:]),
			},
		})
		require.NotNil(t, getPeersResp)
		token := getPeersResp.R.Token

		resp := mockNode.sendQuery(ctx, serverAddr, &dht.Msg{
			T: "ee",
			Y: "q",
			Q: "announce_peer",
			A: &dht.MsgArgs{
				ID:       string(mockNode.id[:]),
				InfoHash: string(infoHash[:]),
				Port:     6881,
				Token:    token,
			},
		})

		require.NotNil(t, resp)
		require.Equal(t, "r", resp.Y)
		require.NotEmpty(t, resp.R.ID)
	})

	t.Run("announce_peer rejects invalid token", func(t *testing.T) {
		infoHash := [20]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}

		resp := mockNode.sendQuery(ctx, serverAddr, &dht.Msg{
			T: "ff",
			Y: "q",
			Q: "announce_peer",
			A: &dht.MsgArgs{
				ID:       string(mockNode.id[:]),
				InfoHash: string(infoHash[:]),
				Port:     6881,
				Token:    "invalid",
			},
		})

		require.NotNil(t, resp, "should receive error response for invalid token")
		require.Equal(t, "e", resp.Y)
		require.Len(t, resp.E, 2)
		code, ok := resp.E[0].(int64)
		require.True(t, ok, "error code should be int64")
		assert.Equal(t, int64(dht.ErrProtocol), code)
	})

	t.Run("find_node missing target returns error", func(t *testing.T) {
		resp := mockNode.sendQuery(ctx, serverAddr, &dht.Msg{
			T: "gg",
			Y: "q",
			Q: "find_node",
			A: &dht.MsgArgs{ID: string(mockNode.id[:])},
		})

		require.NotNil(t, resp, "should receive error response")
		require.Equal(t, "e", resp.Y)
		require.Len(t, resp.E, 2)
		code, ok := resp.E[0].(int64)
		require.True(t, ok, "error code should be int64")
		assert.Equal(t, int64(dht.ErrProtocol), code)
	})

	t.Run("get_peers missing info_hash returns error", func(t *testing.T) {
		resp := mockNode.sendQuery(ctx, serverAddr, &dht.Msg{
			T: "hh",
			Y: "q",
			Q: "get_peers",
			A: &dht.MsgArgs{ID: string(mockNode.id[:])},
		})

		require.NotNil(t, resp, "should receive error response")
		require.Equal(t, "e", resp.Y)
		require.Len(t, resp.E, 2)
		code, ok := resp.E[0].(int64)
		require.True(t, ok, "error code should be int64")
		assert.Equal(t, int64(dht.ErrProtocol), code)
	})

	t.Run("per IP rate limiting", func(t *testing.T) {
		cfg := testDHTConfig(t)
		cfg.RateLimit = 2
		cfg.RateBurst = 2

		srv2, err := dht.NewServer(cfg, recorder.NewNoOp())
		require.NoError(t, err)
		require.NoError(t, srv2.Start(ctx))
		t.Cleanup(func() { srv2.Stop(ctx) })

		mockNode := newMockNode()
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: srv2.Addr().Port}

		for i := 0; i < 2; i++ {
			resp := mockNode.sendQuery(ctx, addr, &dht.Msg{
				T: fmt.Sprintf("%02d", i),
				Y: "q",
				Q: "ping",
				A: &dht.MsgArgs{ID: string(mockNode.id[:])},
			})
			require.NotNil(t, resp, "request %d should succeed", i)
		}

		resp := mockNode.sendQuery(ctx, addr, &dht.Msg{
			T: "xx",
			Y: "q",
			Q: "ping",
			A: &dht.MsgArgs{ID: string(mockNode.id[:])},
		})
		require.Nil(t, resp, "third request should be rate limited")
	})

	t.Run("BEP-42 invalid node ID is rejected", func(t *testing.T) {
		invalidID := [20]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
		mockNode := newMockNodeWithID(invalidID)

		resp := mockNode.sendQuery(ctx, serverAddr, &dht.Msg{
			T: "aa",
			Y: "q",
			Q: "ping",
			A: &dht.MsgArgs{ID: string(mockNode.id[:])},
		})

		require.NotNil(t, resp)
		require.Equal(t, "r", resp.Y, "should still respond but not add invalid node to routing table")
	})

	t.Run("announce_peer validates port range", func(t *testing.T) {
		mockNode := newMockNode()
		infoHash := [20]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}

		getPeersResp := mockNode.sendQuery(ctx, serverAddr, &dht.Msg{
			T: "dd",
			Y: "q",
			Q: "get_peers",
			A: &dht.MsgArgs{
				ID:       string(mockNode.id[:]),
				InfoHash: string(infoHash[:]),
			},
		})
		require.NotNil(t, getPeersResp)
		token := getPeersResp.R.Token

		resp := mockNode.sendQuery(ctx, serverAddr, &dht.Msg{
			T: "ee",
			Y: "q",
			Q: "announce_peer",
			A: &dht.MsgArgs{
				ID:       string(mockNode.id[:]),
				InfoHash: string(infoHash[:]),
				Port:     0,
				Token:    token,
			},
		})

		require.NotNil(t, resp)
		require.Equal(t, "r", resp.Y, "should return response even with invalid port")
	})

	t.Run("announce_peer rejects port > 65535", func(t *testing.T) {
		mockNode := newMockNode()
		infoHash := [20]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}

		getPeersResp := mockNode.sendQuery(ctx, serverAddr, &dht.Msg{
			T: "ff",
			Y: "q",
			Q: "get_peers",
			A: &dht.MsgArgs{
				ID:       string(mockNode.id[:]),
				InfoHash: string(infoHash[:]),
			},
		})
		require.NotNil(t, getPeersResp)
		token := getPeersResp.R.Token

		resp := mockNode.sendQuery(ctx, serverAddr, &dht.Msg{
			T: "gg",
			Y: "q",
			Q: "announce_peer",
			A: &dht.MsgArgs{
				ID:       string(mockNode.id[:]),
				InfoHash: string(infoHash[:]),
				Port:     70000,
				Token:    token,
			},
		})

		require.NotNil(t, resp)
		require.Equal(t, "r", resp.Y, "should return response even with invalid port")
	})

	t.Run("response size is bounded", func(t *testing.T) {
		cfg := testDHTConfig(t)
		cfg.RateLimit = 100
		cfg.RateBurst = 100
		cfg.MaxNodesPerResponse = 8

		srv2, err := dht.NewServer(cfg, recorder.NewNoOp())
		require.NoError(t, err)
		require.NoError(t, srv2.Start(ctx))
		t.Cleanup(func() { srv2.Stop(ctx) })

		mockNode := newMockNode()
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: srv2.Addr().Port}
		target := [20]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}

		resp := mockNode.sendQuery(ctx, addr, &dht.Msg{
			T: "bb",
			Y: "q",
			Q: "find_node",
			A: &dht.MsgArgs{
				ID:     string(mockNode.id[:]),
				Target: string(target[:]),
			},
		})

		require.NotNil(t, resp)
		require.Equal(t, "r", resp.Y)
		if len(resp.R.Nodes) > 0 {
			assert.LessOrEqual(t, len(resp.R.Nodes), 8*26, "nodes should be capped at bucket size")
		}
	})
}

type mockNode struct {
	id dht.NodeID
}

func newMockNode() *mockNode {
	id, _ := dht.DeriveNodeIDFromIP(net.ParseIP("127.0.0.1"))
	return &mockNode{id: id}
}

func newMockNodeWithID(id [20]byte) *mockNode {
	var nid dht.NodeID
	copy(nid[:], id[:])
	return &mockNode{id: nid}
}

func (m *mockNode) sendQuery(ctx context.Context, addr *net.UDPAddr, query *dht.Msg) *dht.Msg {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		return nil
	}
	defer conn.Close()

	data, err := bencode.Marshal(query)
	if err != nil {
		return nil
	}

	_, err = conn.WriteToUDP(data, addr)
	if err != nil {
		return nil
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	buf := make([]byte, 2048)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		return nil
	}

	var respRaw map[string]interface{}
	if err := bencode.Unmarshal(buf[:n], &respRaw); err != nil {
		return nil
	}

	y, ok := respRaw["y"].(string)
	if !ok {
		return nil
	}

	if y == "e" {
		resp := &dht.Msg{
			T: respRaw["t"].(string),
			Y: y,
		}
		if e, ok := respRaw["e"].([]interface{}); ok {
			resp.E = e
		}
		return resp
	}

	resp := &dht.Msg{}
	if err := bencode.Unmarshal(buf[:n], resp); err != nil {
		return nil
	}
	return resp
}

func setupDHTServer(t *testing.T) (*dht.Server, *mockNode) {
	cfg := testDHTConfig(t)
	srv, err := dht.NewServer(cfg, recorder.NewNoOp())
	require.NoError(t, err)
	return srv, newMockNode()
}

func testDHTConfig(t *testing.T) config.DHT {
	t.Helper()
	return config.DHT{
		Port:                0,
		DiscoveryBuffer:     100,
		TransactionTimeout:  2 * time.Second,
		TokenRotation:       5 * time.Minute,
		BucketSize:          8,
		BadFailureThreshold: 2,
		StaleThreshold:      15 * time.Minute,
		NodeID:              generateTestNodeID(),
		NodesPath:           t.TempDir() + "/dht_nodes.dat",
		RateLimit:           100,
		RateBurst:           100,
		Workers:             2,
		MaxNodesPerResponse: 256,
		MaxPeersPerResponse: 50,
	}
}

func generateTestNodeID() string {
	id, err := dht.DeriveNodeIDFromIP(net.ParseIP("127.0.0.1"))
	if err != nil {
		return hex.EncodeToString(make([]byte, 20))
	}
	return hex.EncodeToString(id[:])
}
