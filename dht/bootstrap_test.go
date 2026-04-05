package dht

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	dMocks "github.com/kdwils/mgnx/dht/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// checkBEP42 verifies that id satisfies the BEP-42 invariant for the given IPv4.
func checkBEP42(t *testing.T, ip net.IP, id NodeID) {
	t.Helper()
	ip4 := ip.To4()
	require.NotNil(t, ip4)

	r := id[19] & 0x07
	seed := (binary.BigEndian.Uint32(ip4) & 0x030f3fff) | (uint32(r) << 29)
	var seedBuf [4]byte
	binary.BigEndian.PutUint32(seedBuf[:], seed)
	crc := crc32.Checksum(seedBuf[:], crc32.MakeTable(crc32.Castagnoli))

	assert.Equal(t, byte(crc>>24), id[0], "id[0] must match crc top byte")
	assert.Equal(t, byte(crc>>16), id[1], "id[1] must match crc second byte")
	assert.Equal(t, byte(crc>>8)&0xf8, id[2]&0xf8, "top 5 bits of id[2] must match crc bits 15..11")
}

func TestDeriveBEP42NodeID(t *testing.T) {
	t.Run("satisfies crc32c invariant", func(t *testing.T) {
		ip := net.IP{1, 2, 3, 4}
		id, err := deriveBEP42NodeID(ip)
		require.NoError(t, err)
		checkBEP42(t, ip, id)
	})

	t.Run("last 3 bits of id[19] equal r in seed", func(t *testing.T) {
		ip := net.IP{203, 0, 113, 1}
		id, err := deriveBEP42NodeID(ip)
		require.NoError(t, err)
		// checkBEP42 derives r from id[19]&0x07 and verifies the full CRC round-trip.
		checkBEP42(t, ip, id)
	})

	t.Run("two calls produce different random middle bytes", func(t *testing.T) {
		ip := net.IP{10, 0, 0, 1}
		id1, err := deriveBEP42NodeID(ip)
		require.NoError(t, err)
		id2, err := deriveBEP42NodeID(ip)
		require.NoError(t, err)
		checkBEP42(t, ip, id1)
		checkBEP42(t, ip, id2)
		assert.NotEqual(t, id1, id2)
	})

	t.Run("different IPs produce different CRC prefix", func(t *testing.T) {
		id1, _ := deriveBEP42NodeID(net.IP{1, 2, 3, 4})
		id2, _ := deriveBEP42NodeID(net.IP{5, 6, 7, 8})
		assert.NotEqual(t, [2]byte{id1[0], id1[1]}, [2]byte{id2[0], id2[1]})
	})

	t.Run("non-IPv4 returns zero ID without error", func(t *testing.T) {
		id, err := deriveBEP42NodeID(net.ParseIP("::1"))
		require.NoError(t, err)
		assert.Equal(t, NodeID{}, id)
	})
}

func TestSaveNodeID(t *testing.T) {
	t.Run("writes and reads back 20 bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "dht_id")
		var want NodeID
		for i := range want {
			want[i] = byte(i + 1)
		}
		saveNodeID(path, want)

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		var got NodeID
		copy(got[:], data)
		assert.Equal(t, want, got)
	})

	t.Run("overwrites existing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "dht_id")
		var first NodeID
		first[0] = 0xAA
		saveNodeID(path, first)
		var second NodeID
		second[0] = 0xBB
		saveNodeID(path, second)

		data, _ := os.ReadFile(path)
		assert.Equal(t, byte(0xBB), data[0])
	})

	t.Run("creates parent directories", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "a", "b", "dht_id")
		saveNodeID(path, NodeID{})
		_, err := os.Stat(path)
		assert.NoError(t, err)
	})
}

func serverWithMockResolver(t *testing.T, resolver Resolver) *Server {
	t.Helper()
	s, err := NewServer(testServerCfg(t))
	require.NoError(t, err)
	t.Cleanup(s.Stop)
	s.Resolver = resolver
	return s
}

func TestResolveBootstrapAddrs(t *testing.T) {
	ctx := context.Background()

	t.Run("resolves localhost", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		resolver := dMocks.NewMockResolver(ctrl)
		resolver.EXPECT().LookupHost(gomock.Any(), "localhost").Times(1).Return([]string{"192.168.0.1"}, nil)
		s := serverWithMockResolver(t, resolver)
		addrs := s.resolveBootstrapAddrs(ctx, []string{"localhost:6881"})
		require.NotEmpty(t, addrs)
		assert.Equal(t, 6881, addrs[0].Port)
	})

	t.Run("skips invalid host:port", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		resolver := dMocks.NewMockResolver(ctrl)
		// SplitHostPort fails before LookupHost is reached — no calls expected.
		s := serverWithMockResolver(t, resolver)
		assert.Empty(t, s.resolveBootstrapAddrs(ctx, []string{"nocolon"}))
	})

	t.Run("skips unresolvable hostname", func(t *testing.T) {
		// Stub DNS so no real network call is made — avoids hangs on macOS
		// where the CGO resolver ignores context cancellation deadlines.
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		resolver := dMocks.NewMockResolver(ctrl)
		resolver.EXPECT().LookupHost(gomock.Any(), "this.does.not.exist.invalid").Times(1).Return(nil, &net.DNSError{Err: "no such host", IsNotFound: true})
		s := serverWithMockResolver(t, resolver)
		assert.Empty(t, s.resolveBootstrapAddrs(ctx, []string{"this.does.not.exist.invalid:6881"}))
	})

	t.Run("empty input returns empty slice", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		resolver := dMocks.NewMockResolver(ctrl)
		s := serverWithMockResolver(t, resolver)
		assert.Empty(t, s.resolveBootstrapAddrs(ctx, nil))
	})

	t.Run("resolves multiple entries", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		resolver := dMocks.NewMockResolver(ctrl)
		resolver.EXPECT().LookupHost(gomock.Any(), "localhost").Times(1).Return([]string{"127.0.0.1"}, nil)
		resolver.EXPECT().LookupHost(gomock.Any(), "127.0.0.1").Times(1).Return([]string{"127.0.0.1"}, nil)
		s := serverWithMockResolver(t, resolver)
		addrs := s.resolveBootstrapAddrs(ctx, []string{"localhost:6881", "127.0.0.1:6882"})
		assert.GreaterOrEqual(t, len(addrs), 2)
	})
}

func TestServer_Bootstrap_noAddrs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	s, err := NewServer(testServerCfg(t))
	require.NoError(t, err)
	defer s.Stop()
	require.NoError(t, s.Start(ctx))

	assert.NoError(t, s.Bootstrap(ctx, nil))
}

func TestServer_Bootstrap_seedNode(t *testing.T) {
	t.Run("seed is added to routing table after bootstrap", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		seed, err := NewServer(testServerCfg(t))
		require.NoError(t, err)
		defer seed.Stop()
		require.NoError(t, seed.Start(ctx))

		client, err := NewServer(testServerCfg(t))
		require.NoError(t, err)
		defer client.Stop()
		require.NoError(t, client.Start(ctx))

		seedAddr := fmt.Sprintf("127.0.0.1:%d", seed.conn.LocalAddr().(*net.UDPAddr).Port)
		require.NoError(t, client.Bootstrap(ctx, []string{seedAddr}))

		closest := client.table.Closest(seed.ourID, 1)
		require.Len(t, closest, 1)
		assert.Equal(t, seed.ourID, closest[0].ID)
	})
}

func TestServer_Bootstrap_bep42(t *testing.T) {
	t.Run("node ID is updated when response carries external IP", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		externalIP := net.IP{1, 2, 3, 4}
		seed := newIPEchoServer(t, externalIP)
		defer seed.Close()

		client, err := NewServer(testServerCfg(t))
		require.NoError(t, err)
		defer client.Stop()
		require.NoError(t, client.Start(ctx))

		originalID := client.ourID
		seedAddr := fmt.Sprintf("127.0.0.1:%d", seed.LocalAddr().(*net.UDPAddr).Port)
		require.NoError(t, client.Bootstrap(ctx, []string{seedAddr}))

		assert.NotEqual(t, originalID, client.ourID)
		checkBEP42(t, externalIP, client.ourID)
	})
}

// newIPEchoServer starts a minimal UDP listener that responds to any message
// with a KRPC response carrying the given IP field (simulating a remote node
// reporting our external IP per BEP-42). The caller must call Close() on the
// returned conn when done.
func newIPEchoServer(t *testing.T, externalIP net.IP) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	udpConn := conn.(*net.UDPConn)

	var fakeID NodeID
	fakeID[0] = 0x77

	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := udpConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			var req Msg
			if err := bencode.Unmarshal(buf[:n], &req); err != nil {
				continue
			}
			resp := Msg{	
				T:  req.T,
				Y:  "r",
				IP: string(externalIP.To4()),
				R:  &Return{ID: string(fakeID[:])},
			}
			data, err := bencode.Marshal(resp)
			if err != nil {
				continue
			}
			udpConn.WriteToUDP(data, addr) //nolint:errcheck
		}
	}()

	return udpConn
}
