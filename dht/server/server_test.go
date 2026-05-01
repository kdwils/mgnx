package server

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/dht/filter"
	"github.com/kdwils/mgnx/dht/krpc"
	mockconn "github.com/kdwils/mgnx/dht/mocks/conn"
	mockresolver "github.com/kdwils/mgnx/dht/mocks/resolver"
	"github.com/kdwils/mgnx/dht/table"
	"github.com/kdwils/mgnx/dht/types"
	"github.com/kdwils/mgnx/recorder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func setupServer(t *testing.T, ctrl *gomock.Controller) (*Server, *mockconn.MockConn) {
	conn := mockconn.NewMockConn(ctrl)
	rec := recorder.NewNoOp()
	cfg := config.DHT{
		NodeID:                   "0000000000000000000000000000000000000000",
		BucketSize:               8,
		TokenRotation:            5 * time.Minute,
		RateLimit:                1000,
		RateBurst:                2000,
		MaxNodesPerResponse:      8,
		MaxPeersPerResponse:      50,
		IPLimiterMaxSize:         1000,
		BadFailureThreshold:      3,
		StaleThreshold:           1 * time.Hour,
		PeerStoreTTL:             5 * time.Minute,
		PeerStoreMaxEntries:      1000,
		PeerStoreMaxPeersPerHash: 50,
		DiscoveryBuffer:          100,
		TransactionTimeout:       1 * time.Second,
		BucketRefreshInterval:    1 * time.Hour,
		WarmBootstrapThreshold:   1,
		BloomN:                   1000,
		BloomP:                   0.01,
		BloomRotation:            1 * time.Hour,
	}
	discovered := make(chan types.DiscoveredPeers, cfg.DiscoveryBuffer)
	dedup := filter.NewBloomFilter(cfg.BloomN, cfg.BloomP, cfg.BloomRotation)
	s, err := NewServer(cfg, nil, conn, rec, discovered, dedup)
	require.NoError(t, err)
	s.peerStore.now = func() time.Time { return time.Unix(1000, 0) }
	return s, conn
}

func TestNewServer(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	conn := mockconn.NewMockConn(ctrl)
	rec := recorder.NewNoOp()

	t.Run("explicit node ID", func(t *testing.T) {
		cfg := config.DHT{NodeID: "0123456789abcdef0123456789abcdef01234567"}
		discovered := make(chan types.DiscoveredPeers, 100)
		dedup := filter.NewBloomFilter(1000, 0.01, time.Hour)
		s, err := NewServer(cfg, nil, conn, rec, discovered, dedup)
		require.NoError(t, err)
		want, _ := table.ParseNodeIDHex(cfg.NodeID)
		assert.Equal(t, want, s.nodeID)
	})

	t.Run("IP derived node ID", func(t *testing.T) {
		cfg := config.DHT{}
		ip := net.ParseIP("1.2.3.4")
		discovered := make(chan types.DiscoveredPeers, 100)
		dedup := filter.NewBloomFilter(1000, 0.01, time.Hour)
		s, err := NewServer(cfg, ip, conn, rec, discovered, dedup)
		require.NoError(t, err)
		err = table.ValidateNodeIDForIP(ip, s.nodeID)
		assert.NoError(t, err)
	})
}

func TestServer_handlePing(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	t.Run("successful ping response", func(t *testing.T) {
		s, _ := setupServer(t, ctrl)
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
		msg := &krpc.Msg{
			T: "aa",
			Q: "ping",
			A: &krpc.MsgArgs{ID: "remoteid000000000000"},
		}
		s.handlePing(context.Background(), addr, msg)

		select {
		case got := <-s.outbound:
			want := &outMsg{
				addr: addr,
				msg: &krpc.Msg{
					T: "aa",
					Y: "r",
					R: &krpc.Return{ID: string(s.nodeID[:])},
				},
			}
			assert.Equal(t, want, got)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("no message enqueued to outbound")
		}
	})
}

func TestServer_handleFindNode(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	t.Run("missing arguments", func(t *testing.T) {
		s, _ := setupServer(t, ctrl)
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
		msg := &krpc.Msg{T: "aa", Q: "find_node"}
		s.handleFindNode(context.Background(), addr, msg)
		select {
		case got := <-s.outbound:
			want := &outMsg{
				addr: addr,
				msg: &krpc.Msg{
					T: "aa",
					Y: "e",
					E: []any{int64(krpc.ErrProtocol), "missing arguments"},
				},
			}
			assert.Equal(t, want, got)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("no message enqueued to outbound")
		}
	})

	t.Run("success with target", func(t *testing.T) {
		s, _ := setupServer(t, ctrl)
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}

		node1ID := table.NodeID{1}
		node1 := &table.Node{ID: node1ID, Addr: &net.UDPAddr{IP: net.ParseIP("1.1.1.1"), Port: 1111}, LastSeen: time.Unix(1000, 0)}
		s.InsertNode(context.Background(), node1)

		target := table.NodeID{1}
		msg := &krpc.Msg{
			T: "ab",
			Q: "find_node",
			A: &krpc.MsgArgs{ID: "remoteid000000000000", Target: string(target[:])},
		}
		s.handleFindNode(context.Background(), addr, msg)

		select {
		case got := <-s.outbound:
			closest := s.table.Closest(target, s.bucketSize)
			require.Equal(t, 1, len(closest))

			want := &outMsg{
				addr: addr,
				msg: &krpc.Msg{
					T: "ab",
					Y: "r",
					R: &krpc.Return{
						ID:    string(s.nodeID[:]),
						Nodes: table.EncodeNodes(closest),
					},
				},
			}
			assert.Equal(t, want, got)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("no message enqueued to outbound")
		}
	})
}

func TestServer_handleGetPeers(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	t.Run("return values", func(t *testing.T) {
		s, _ := setupServer(t, ctrl)
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}

		infoHash := table.NodeID{2}
		peerIP := net.ParseIP("5.5.5.5")
		peerPort := 5555
		s.peerStore.Add(infoHash, peerIP, peerPort)

		msg := &krpc.Msg{
			T: "aa",
			Q: "get_peers",
			A: &krpc.MsgArgs{ID: "remoteid000000000000", InfoHash: string(infoHash[:])},
		}
		s.handleGetPeers(context.Background(), addr, msg)

		select {
		case got := <-s.outbound:
			token := s.token.Generate(addr.IP)
			want := &outMsg{
				addr: addr,
				msg: &krpc.Msg{
					T: "aa",
					Y: "r",
					R: &krpc.Return{
						ID:     string(s.nodeID[:]),
						Values: []string{table.EncodePeer(peerIP, peerPort)},
						Token:  token,
					},
				},
			}
			assert.Equal(t, want, got)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("no message enqueued to outbound")
		}
	})

	t.Run("return nodes", func(t *testing.T) {
		s, _ := setupServer(t, ctrl)
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}

		otherHash := table.NodeID{3}
		node1 := &table.Node{ID: table.NodeID{1}, Addr: &net.UDPAddr{IP: net.ParseIP("1.1.1.1"), Port: 1111}, LastSeen: time.Unix(1000, 0)}
		s.InsertNode(context.Background(), node1)

		msg := &krpc.Msg{
			T: "ab",
			Q: "get_peers",
			A: &krpc.MsgArgs{ID: "remoteid000000000000", InfoHash: string(otherHash[:])},
		}
		s.handleGetPeers(context.Background(), addr, msg)

		select {
		case got := <-s.outbound:
			token := s.token.Generate(addr.IP)
			closest := s.table.Closest(otherHash, s.bucketSize)
			require.Equal(t, 1, len(closest))

			want := &outMsg{
				addr: addr,
				msg: &krpc.Msg{
					T: "ab",
					Y: "r",
					R: &krpc.Return{
						ID:    string(s.nodeID[:]),
						Nodes: table.EncodeNodes(closest),
						Token: token,
					},
				},
			}
			assert.Equal(t, want, got)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("no message enqueued to outbound")
		}
	})
}

func TestServer_handleAnnouncePeer(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	t.Run("success", func(t *testing.T) {
		s, _ := setupServer(t, ctrl)
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
		token := s.token.Generate(addr.IP)
		infoHash := table.NodeID{2}

		port := 6666
		msg := &krpc.Msg{
			T: "aa",
			Q: "announce_peer",
			A: &krpc.MsgArgs{
				ID:       "remoteid000000000000",
				InfoHash: string(infoHash[:]),
				Port:     port,
				Token:    token,
			},
		}
		s.handleAnnouncePeer(context.Background(), addr, msg)

		// Check response
		select {
		case got := <-s.outbound:
			want := &outMsg{
				addr: addr,
				msg: &krpc.Msg{
					T: "aa",
					Y: "r",
					R: &krpc.Return{ID: string(s.nodeID[:])},
				},
			}
			assert.Equal(t, want, got)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("no response enqueued")
		}

		// Check peerStore
		peers := s.peerStore.Get(infoHash)
		require.Equal(t, 1, len(peers))
		assert.True(t, peers[0].IP.Equal(addr.IP))
		assert.Equal(t, port, peers[0].Port)

		// Check discovered channel
		select {
		case event := <-s.discovered:
			assert.Equal(t, [20]byte(infoHash), [20]byte(event.Infohash))
			require.Equal(t, 1, len(event.Peers))
			assert.True(t, event.Peers[0].SourceIP.Equal(addr.IP))
			assert.Equal(t, port, event.Peers[0].Port)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("no event emitted to discovered channel")
		}
	})

	t.Run("invalid token", func(t *testing.T) {
		s, _ := setupServer(t, ctrl)
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
		infoHash := table.NodeID{2}

		msg := &krpc.Msg{
			T: "ab",
			Q: "announce_peer",
			A: &krpc.MsgArgs{
				ID:       "remoteid000000000000",
				InfoHash: string(infoHash[:]),
				Port:     7777,
				Token:    "badtoken",
			},
		}
		s.handleAnnouncePeer(context.Background(), addr, msg)
		select {
		case got := <-s.outbound:
			want := &outMsg{
				addr: addr,
				msg: &krpc.Msg{
					T: "ab",
					Y: "e",
					E: []any{int64(krpc.ErrProtocol), "bad token"},
				},
			}
			assert.Equal(t, want, got)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("no response enqueued")
		}
	})

	t.Run("implied port", func(t *testing.T) {
		s, _ := setupServer(t, ctrl)
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
		token := s.token.Generate(addr.IP)
		infoHash := table.NodeID{2}

		implied := 1
		msg := &krpc.Msg{
			T: "ac",
			Q: "announce_peer",
			A: &krpc.MsgArgs{
				ID:          "remoteid000000000000",
				InfoHash:    string(infoHash[:]),
				Port:        7777,
				Token:       token,
				ImpliedPort: &implied,
			},
		}
		s.handleAnnouncePeer(context.Background(), addr, msg)

		select {
		case <-s.outbound:
		case <-time.After(100 * time.Millisecond):
			t.Fatal("no response enqueued")
		}

		peers := s.peerStore.Get(infoHash)
		require.Equal(t, 1, len(peers))
		assert.Equal(t, addr.Port, peers[0].Port)
	})
}

func TestServer_handleSampleInfohashes(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	t.Run("success", func(t *testing.T) {
		s, _ := setupServer(t, ctrl)
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
		target := table.NodeID{1}

		node1ID := table.NodeID{1}
		node1 := &table.Node{ID: node1ID, Addr: &net.UDPAddr{IP: net.ParseIP("1.1.1.1"), Port: 1111}, LastSeen: time.Unix(1000, 0)}
		s.InsertNode(context.Background(), node1)

		msg := &krpc.Msg{
			T: "aa",
			Q: "sample_infohashes",
			A: &krpc.MsgArgs{
				ID:     "remoteid000000000000",
				Target: string(target[:]),
			},
		}
		s.handleSampleInfohashes(context.Background(), addr, msg)

		select {
		case got := <-s.outbound:
			closest := s.table.Closest(target, s.bucketSize)
			want := &outMsg{
				addr: addr,
				msg: &krpc.Msg{
					T: "aa",
					Y: "r",
					R: &krpc.Return{
						ID:    string(s.nodeID[:]),
						Nodes: table.EncodeNodes(closest),
					},
				},
			}
			assert.Equal(t, want, got)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("no response enqueued")
		}
	})
}

func TestServer_processQuery(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	t.Run("ping query", func(t *testing.T) {
		s, _ := setupServer(t, ctrl)
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
		id := table.NodeID{10}
		msg := &krpc.Msg{
			T: "aa",
			Q: "ping",
			A: &krpc.MsgArgs{ID: string(id[:])},
		}

		s.processQuery(context.Background(), inMsg{addr: addr, msg: msg})

		select {
		case got := <-s.outbound:
			assert.Equal(t, "r", got.msg.Y)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("no response enqueued")
		}

		assert.Equal(t, 1, s.NodeCount())
	})
}

func TestServer_routeMessage(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	t.Run("route query to handlers", func(t *testing.T) {
		s, _ := setupServer(t, ctrl)
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
		msg := &krpc.Msg{T: "aa", Y: "q", Q: "ping"}
		s.routeMessage(context.Background(), addr, msg)

		select {
		case in := <-s.handlers:
			assert.Equal(t, addr, in.addr)
			assert.Equal(t, "aa", in.msg.T)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("query not routed to handlers")
		}
	})

	t.Run("route response to txns", func(t *testing.T) {
		s, _ := setupServer(t, ctrl)
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
		nodeID := table.NodeID{1}
		txn := s.txns.New(nodeID, addr)

		msg := &krpc.Msg{T: txn.ID, Y: "r", R: &krpc.Return{ID: string(nodeID[:])}}
		s.routeMessage(context.Background(), addr, msg)

		select {
		case resp := <-txn.Response:
			assert.Equal(t, txn.ID, resp.T)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("response not routed to txn")
		}
	})
}

func TestServer_updateTableFromResponse(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	t.Run("success", func(t *testing.T) {
		s, _ := setupServer(t, ctrl)
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
		nodeID := table.NodeID{1}

		msg := &krpc.Msg{
			Y: "r",
			R: &krpc.Return{ID: string(nodeID[:])},
		}
		s.updateTableFromResponse(context.Background(), addr, msg)

		assert.Equal(t, 1, s.NodeCount())

		closest := s.Closest(nodeID, 1)
		require.Equal(t, 1, len(closest))
		assert.Equal(t, nodeID, closest[0].ID)
	})
}

func TestServer_send(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	t.Run("success", func(t *testing.T) {
		s, _ := setupServer(t, ctrl)
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
		nodeID := table.NodeID{1}
		msg := &krpc.Msg{Y: "q", Q: "ping", A: &krpc.MsgArgs{ID: string(s.nodeID[:])}}

		go func() {
			select {
			case out := <-s.outbound:
				resp := &krpc.Msg{T: out.msg.T, Y: "r", R: &krpc.Return{ID: string(nodeID[:])}}
				s.txns.Complete(out.msg.T, resp)
			case <-time.After(1 * time.Second):
				return
			}
		}()

		resp, err := s.send(context.Background(), addr, nodeID, msg)
		require.NoError(t, err)
		assert.Equal(t, "r", resp.Y)
		require.NotNil(t, resp.R)
		assert.Equal(t, string(nodeID[:]), resp.R.ID)
	})

	t.Run("timeout", func(t *testing.T) {
		s, _ := setupServer(t, ctrl)
		addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
		nodeID := table.NodeID{1}
		msg := &krpc.Msg{Y: "q", Q: "ping", A: &krpc.MsgArgs{ID: string(s.nodeID[:])}}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()

		_, err := s.send(ctx, addr, nodeID, msg)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestServer_Bootstrap(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	t.Run("successful bootstrap", func(t *testing.T) {
		s, _ := setupServer(t, ctrl)
		mockResolver := mockresolver.NewMockResolver(ctrl)
		s.Resolver = mockResolver

		bootstrapNode := "router.bittorrent.com:6881"
		bootstrapIP := "67.215.246.10"

		mockResolver.EXPECT().LookupHost(gomock.Any(), "router.bittorrent.com").Return([]string{bootstrapIP}, nil)

		nodeID1 := table.NodeID{1}
		node1 := &table.Node{ID: nodeID1, Addr: &net.UDPAddr{IP: net.ParseIP("1.1.1.1"), Port: 1111}}

		go func() {
			// querySeeds sends a findNode
			select {
			case out := <-s.outbound:
				resp := &krpc.Msg{
					T: out.msg.T,
					Y: "r",
					R: &krpc.Return{
						ID:    string(nodeID1[:]),
						Nodes: table.EncodeNodes([]*table.Node{node1}),
					},
				}
				s.txns.Complete(out.msg.T, resp)
			case <-time.After(1 * time.Second):
				return
			}

			// convergeTable will send another findNode to node1
			select {
			case out := <-s.outbound:
				resp := &krpc.Msg{
					T: out.msg.T,
					Y: "r",
					R: &krpc.Return{
						ID:    string(node1.ID[:]),
						Nodes: "",
					},
				}
				s.txns.Complete(out.msg.T, resp)
			case <-time.After(1 * time.Second):
				return
			}
		}()

		err := s.Bootstrap(context.Background(), []string{bootstrapNode})
		require.NoError(t, err)

		assert.Equal(t, 1, s.NodeCount())
		closest := s.Closest(nodeID1, 1)
		require.Equal(t, 1, len(closest))
		assert.Equal(t, nodeID1, closest[0].ID)
	})
}
