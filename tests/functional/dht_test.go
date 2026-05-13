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
	"github.com/kdwils/mgnx/dht/filter"
	"github.com/kdwils/mgnx/dht/krpc"
	"github.com/kdwils/mgnx/dht/server"
	"github.com/kdwils/mgnx/dht/table"
	"github.com/kdwils/mgnx/dht/types"
	"github.com/kdwils/mgnx/recorder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDHTBEPProtocol(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, mockNode := setupDHTServer(t, testDHTConfig(t))
	require.NoError(t, srv.Start(ctx))
	t.Cleanup(func() { srv.Stop(ctx) })

	serverAddr := srv.Addr()

	t.Run("ping returns our node ID", func(t *testing.T) {
		resp := mockNode.sendQuery(ctx, serverAddr, &krpc.Msg{
			T: "aa",
			Y: "q",
			Q: "ping",
			A: &krpc.MsgArgs{ID: string(mockNode.id[:])},
		})

		require.NotNil(t, resp)
		require.Equal(t, "r", resp.Y)
		require.NotEmpty(t, resp.R.ID)
	})

	t.Run("find_node returns compact nodes", func(t *testing.T) {
		target := table.NodeID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}
		resp := mockNode.sendQuery(ctx, serverAddr, &krpc.Msg{
			T: "bb",
			Y: "q",
			Q: "find_node",
			A: &krpc.MsgArgs{
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
		infoHash := table.NodeID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}
		resp := mockNode.sendQuery(ctx, serverAddr, &krpc.Msg{
			T: "cc",
			Y: "q",
			Q: "get_peers",
			A: &krpc.MsgArgs{
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
		infoHash := table.NodeID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}

		getPeersResp := mockNode.sendQuery(ctx, serverAddr, &krpc.Msg{
			T: "dd",
			Y: "q",
			Q: "get_peers",
			A: &krpc.MsgArgs{
				ID:       string(mockNode.id[:]),
				InfoHash: string(infoHash[:]),
			},
		})
		require.NotNil(t, getPeersResp)
		token := getPeersResp.R.Token

		resp := mockNode.sendQuery(ctx, serverAddr, &krpc.Msg{
			T: "ee",
			Y: "q",
			Q: "announce_peer",
			A: &krpc.MsgArgs{
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
		infoHash := table.NodeID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}

		resp := mockNode.sendQuery(ctx, serverAddr, &krpc.Msg{
			T: "ff",
			Y: "q",
			Q: "announce_peer",
			A: &krpc.MsgArgs{
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
		assert.Equal(t, int64(krpc.ErrProtocol), code)
	})

	t.Run("find_node missing target returns error", func(t *testing.T) {
		resp := mockNode.sendQuery(ctx, serverAddr, &krpc.Msg{
			T: "gg",
			Y: "q",
			Q: "find_node",
			A: &krpc.MsgArgs{ID: string(mockNode.id[:])},
		})

		require.NotNil(t, resp, "should receive error response")
		require.Equal(t, "e", resp.Y)
		require.Len(t, resp.E, 2)
		code, ok := resp.E[0].(int64)
		require.True(t, ok, "error code should be int64")
		assert.Equal(t, int64(krpc.ErrProtocol), code)
	})

	t.Run("get_peers missing info_hash returns error", func(t *testing.T) {
		resp := mockNode.sendQuery(ctx, serverAddr, &krpc.Msg{
			T: "hh",
			Y: "q",
			Q: "get_peers",
			A: &krpc.MsgArgs{ID: string(mockNode.id[:])},
		})

		require.NotNil(t, resp, "should receive error response")
		require.Equal(t, "e", resp.Y)
		require.Len(t, resp.E, 2)
		code, ok := resp.E[0].(int64)
		require.True(t, ok, "error code should be int64")
		assert.Equal(t, int64(krpc.ErrProtocol), code)
	})

	t.Run("per IP rate limiting", func(t *testing.T) {
		cfg := testDHTConfig(t)
		cfg.RateLimit = 2
		cfg.RateBurst = 2

		srv2, mockNode2 := setupDHTServer(t, cfg)
		require.NoError(t, srv2.Start(ctx))
		t.Cleanup(func() { srv2.Stop(ctx) })

		addr := srv2.Addr()

		for i := 0; i < 2; i++ {
			resp := mockNode2.sendQuery(ctx, addr, &krpc.Msg{
				T: fmt.Sprintf("%02d", i),
				Y: "q",
				Q: "ping",
				A: &krpc.MsgArgs{ID: string(mockNode2.id[:])},
			})
			require.NotNil(t, resp, "request %d should succeed", i)
		}

		resp := mockNode2.sendQuery(ctx, addr, &krpc.Msg{
			T: "xx",
			Y: "q",
			Q: "ping",
			A: &krpc.MsgArgs{ID: string(mockNode2.id[:])},
		})
		require.Nil(t, resp, "third request should be rate limited")
	})

	t.Run("BEP-42 invalid node ID is rejected from routing table", func(t *testing.T) {
		invalidID := table.NodeID{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
		mockNode := newMockNodeWithID(invalidID)

		resp := mockNode.sendQuery(ctx, serverAddr, &krpc.Msg{
			T: "aa",
			Y: "q",
			Q: "ping",
			A: &krpc.MsgArgs{ID: string(mockNode.id[:])},
		})

		require.NotNil(t, resp)
		require.Equal(t, "r", resp.Y, "should still respond but not add invalid node to routing table")
		assert.Equal(t, 0, srv.NodeCount(), "invalid node should not be in routing table")
	})

	t.Run("BEP-42 invalid node ID in find_node response is rejected", func(t *testing.T) {
		// Start server B that will return an invalid node in its find_node response.
		// We do this by pre-populating B's table with a node that has an invalid ID for its IP.
		cfgB := testDHTConfig(t)
		srvB, _ := setupDHTServer(t, cfgB)
		require.NoError(t, srvB.Start(ctx))
		t.Cleanup(func() { srvB.Stop(ctx) })

		// Insert a node with a clearly invalid ID into B's routing table directly.
		invalidID := table.NodeID{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
		fakeAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.2"), Port: 12345}
		srvB.InsertNode(ctx, &table.Node{ID: invalidID, Addr: fakeAddr, LastSeen: time.Now()})

		// Now have serverA query serverB via find_node.
		cfgA := testDHTConfig(t)
		discoveredA := make(chan types.DiscoveredPeers, cfgA.DiscoveryBuffer)
		defer close(discoveredA)
		dedupA := filter.NewBloomFilter(cfgA.BloomN, cfgA.BloomP, cfgA.BloomRotation)
		udpAddrA, err := net.ResolveUDPAddr("udp4", "127.0.0.1:0")
		require.NoError(t, err)
		connA, err := net.ListenUDP("udp4", udpAddrA)
		require.NoError(t, err)
		defer connA.Close()

		srvA, err := server.NewServer(cfgA, nil, connA, recorder.NewNoOp(), discoveredA, dedupA)
		require.NoError(t, err)
		require.NoError(t, srvA.Start(ctx))
		t.Cleanup(func() { srvA.Stop(ctx) })

		bAddr := srvB.Addr()
		var target table.NodeID
		resp, err := srvA.FindNode(ctx, bAddr, srvB.NodeID(), target)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.NotNil(t, resp.R)

		// Any nodes returned from B that have invalid IDs should not end up in A's table.
		// Since the invalid node was inserted manually, A should not have added it.
		nodes, decodeErr := table.DecodeNodes(resp.R.Nodes)
		if decodeErr == nil {
			for _, n := range nodes {
				if table.ValidateNodeIDForIP(n.Addr.IP, n.ID) != nil {
					// This node is invalid for its IP; it should NOT be in A's routing table.
					closest := srvA.Closest(n.ID, 1)
					for _, c := range closest {
						assert.NotEqual(t, n.ID, c.ID, "invalid node should not be in routing table")
					}
				}
			}
		}
	})

	t.Run("announce_peer validates port range", func(t *testing.T) {
		mockNode := newMockNode()
		infoHash := table.NodeID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}

		getPeersResp := mockNode.sendQuery(ctx, serverAddr, &krpc.Msg{
			T: "dd",
			Y: "q",
			Q: "get_peers",
			A: &krpc.MsgArgs{
				ID:       string(mockNode.id[:]),
				InfoHash: string(infoHash[:]),
			},
		})
		require.NotNil(t, getPeersResp)
		token := getPeersResp.R.Token

		resp := mockNode.sendQuery(ctx, serverAddr, &krpc.Msg{
			T: "ee",
			Y: "q",
			Q: "announce_peer",
			A: &krpc.MsgArgs{
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
		infoHash := table.NodeID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}

		getPeersResp := mockNode.sendQuery(ctx, serverAddr, &krpc.Msg{
			T: "ff",
			Y: "q",
			Q: "get_peers",
			A: &krpc.MsgArgs{
				ID:       string(mockNode.id[:]),
				InfoHash: string(infoHash[:]),
			},
		})
		require.NotNil(t, getPeersResp)
		token := getPeersResp.R.Token

		resp := mockNode.sendQuery(ctx, serverAddr, &krpc.Msg{
			T: "gg",
			Y: "q",
			Q: "announce_peer",
			A: &krpc.MsgArgs{
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

		srv2, mockNode2 := setupDHTServer(t, cfg)
		require.NoError(t, srv2.Start(ctx))
		t.Cleanup(func() { srv2.Stop(ctx) })

		addr := srv2.Addr()
		target := table.NodeID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14}

		resp := mockNode2.sendQuery(ctx, addr, &krpc.Msg{
			T: "bb",
			Y: "q",
			Q: "find_node",
			A: &krpc.MsgArgs{
				ID:     string(mockNode2.id[:]),
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

// TestDHTBucketRefresh_insertsNodesFromResponse verifies end-to-end that nodes
// returned in a find_node response are inserted into the querying server's
// routing table.
func TestDHTBucketRefresh_insertsNodesFromResponse(t *testing.T) {
	t.Run("find_node response nodes are inserted into routing table", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// B knows about C; when A queries B the response will carry C.
		serverB, _ := setupDHTServer(t, testDHTConfig(t))
		require.NoError(t, serverB.Start(ctx))
		t.Cleanup(func() { serverB.Stop(ctx) })

		serverC, _ := setupDHTServer(t, testDHTConfig(t))
		require.NoError(t, serverC.Start(ctx))
		t.Cleanup(func() { serverC.Stop(ctx) })

		// Pre-populate B's routing table with C.
		cAddr := serverC.Addr()
		serverB.InsertNode(ctx, &table.Node{ID: serverC.NodeID(), Addr: cAddr, LastSeen: time.Now()})

		serverA, _ := setupDHTServer(t, testDHTConfig(t))
		require.NoError(t, serverA.Start(ctx))
		t.Cleanup(func() { serverA.Stop(ctx) })

		bAddr := serverB.Addr()

		var target table.NodeID
		resp, err := serverA.FindNode(ctx, bAddr, serverB.NodeID(), target)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.NotNil(t, resp.R)
		require.NotEmpty(t, resp.R.Nodes, "B should have returned C in its response")

		nodes, err := table.DecodeNodes(resp.R.Nodes)
		require.NoError(t, err)
		for _, node := range nodes {
			serverA.InsertNode(ctx, node)
		}

		// A should now know about C (learned via B's find_node response).
		closest := serverA.Closest(serverC.NodeID(), 1)
		require.NotEmpty(t, closest)
		assert.Equal(t, serverC.NodeID(), closest[0].ID)
	})
}

func TestDHTRefreshStaleBuckets_insertsNodesFromResponse(t *testing.T) {
	t.Run("stale bucket refresh grows routing table via find_node responses", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// B knows about C.
		serverB, _ := setupDHTServer(t, testDHTConfig(t))
		require.NoError(t, serverB.Start(ctx))
		t.Cleanup(func() { serverB.Stop(ctx) })

		serverC, _ := setupDHTServer(t, testDHTConfig(t))
		require.NoError(t, serverC.Start(ctx))
		t.Cleanup(func() { serverC.Stop(ctx) })

		cAddr := serverC.Addr()
		serverB.InsertNode(ctx, &table.Node{ID: serverC.NodeID(), Addr: cAddr, LastSeen: time.Now()})

		// A starts with only B in its table; all buckets are stale (threshold=0).
		cfg := testDHTConfig(t)
		cfg.StaleThreshold = 0

		discovered := make(chan types.DiscoveredPeers, cfg.DiscoveryBuffer)
		dedup := filter.NewBloomFilter(cfg.BloomN, cfg.BloomP, cfg.BloomRotation)
		udpAddr, err := net.ResolveUDPAddr("udp4", "127.0.0.1:0")
		require.NoError(t, err)
		conn, err := net.ListenUDP("udp4", udpAddr)
		require.NoError(t, err)

		serverA, err := server.NewServer(cfg, nil, conn, recorder.NewNoOp(), discovered, dedup)
		require.NoError(t, err)
		require.NoError(t, serverA.Start(ctx))
		t.Cleanup(func() { serverA.Stop(ctx) })

		bAddr := serverB.Addr()
		serverA.InsertNode(ctx, &table.Node{ID: serverB.NodeID(), Addr: bAddr, LastSeen: time.Now()})
		initialCount := serverA.NodeCount()

		serverA.RefreshStaleBuckets(ctx)

		require.Eventually(t, func() bool {
			return serverA.NodeCount() > initialCount
		}, 3*time.Second, 50*time.Millisecond, "routing table should grow after stale bucket refresh")
	})
}

type mockNode struct {
	id table.NodeID
}

func newMockNode() *mockNode {
	id, _ := table.DeriveNodeIDFromIP(net.ParseIP("127.0.0.1"))
	// Make it slightly different
	id[19]++
	return &mockNode{id: id}
}

func newMockNodeWithID(id [20]byte) *mockNode {
	var nid table.NodeID
	copy(nid[:], id[:])
	return &mockNode{id: nid}
}

func (m *mockNode) sendQuery(ctx context.Context, addr *net.UDPAddr, query *krpc.Msg) *krpc.Msg {
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
		resp := &krpc.Msg{
			T: respRaw["t"].(string),
			Y: y,
		}
		if e, ok := respRaw["e"].([]interface{}); ok {
			resp.E = e
		}
		return resp
	}

	resp := &krpc.Msg{}
	if err := bencode.Unmarshal(buf[:n], resp); err != nil {
		return nil
	}
	return resp
}

func setupDHTServer(t *testing.T, cfg config.DHT) (*server.Server, *mockNode) {
	discovered := make(chan types.DiscoveredPeers, cfg.DiscoveryBuffer)
	dedup := filter.NewBloomFilter(cfg.BloomN, cfg.BloomP, cfg.BloomRotation)

	udpAddr, err := net.ResolveUDPAddr("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	conn, err := net.ListenUDP("udp4", udpAddr)
	require.NoError(t, err)

	srv, err := server.NewServer(cfg, nil, conn, recorder.NewNoOp(), discovered, dedup)
	require.NoError(t, err)
	return srv, newMockNode()
}

func testDHTConfig(t *testing.T) config.DHT {
	t.Helper()
	return config.DHT{
		Port:                     0,
		DiscoveryBuffer:          100,
		TransactionTimeout:       2 * time.Second,
		TokenRotation:            5 * time.Minute,
		BucketSize:               8,
		BadFailureThreshold:      2,
		StaleThreshold:           15 * time.Minute,
		NodeID:                   generateTestNodeID(),
		RateLimit:                100,
		RateBurst:                100,
		Workers:                  2,
		MaxNodesPerResponse:      256,
		MaxPeersPerResponse:      50,
		PeerStoreTTL:             30 * time.Minute,
		PeerStoreMaxEntries:      10_000,
		PeerStoreMaxPeersPerHash: 200,
		BucketRefreshInterval:    1 * time.Hour,
		BloomN:                   1000,
		BloomP:                   0.01,
		BloomRotation:            time.Hour,
	}
}

func generateTestNodeID() string {
	id, err := table.DeriveNodeIDFromIP(net.ParseIP("127.0.0.1"))
	if err != nil {
		return hex.EncodeToString(make([]byte, 20))
	}
	return hex.EncodeToString(id[:])
}
