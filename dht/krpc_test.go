package dht

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeNodes(t *testing.T) {
	t.Run("encodes single node to 26 bytes", func(t *testing.T) {
		n := &Node{
			ID:   NodeID{0x01},
			Addr: &net.UDPAddr{IP: net.IP{1, 2, 3, 4}, Port: 6881},
		}
		got := EncodeNodes([]*Node{n})
		assert.Len(t, got, 26)
	})

	t.Run("encodes known bytes correctly", func(t *testing.T) {
		var id NodeID
		id[0] = 0xAB
		n := &Node{
			ID:   id,
			Addr: &net.UDPAddr{IP: net.IP{10, 0, 0, 1}, Port: 0x1A2B},
		}
		got := []byte(EncodeNodes([]*Node{n}))

		assert.Equal(t, byte(0xAB), got[0])
		assert.Equal(t, []byte{10, 0, 0, 1}, got[20:24])
		assert.Equal(t, []byte{0x1A, 0x2B}, got[24:26])
	})

	t.Run("skips non-IPv4 nodes", func(t *testing.T) {
		n := &Node{
			ID:   NodeID{},
			Addr: &net.UDPAddr{IP: net.ParseIP("::1"), Port: 6881},
		}
		got := EncodeNodes([]*Node{n})
		assert.Len(t, got, 0)
	})

	t.Run("encodes multiple nodes as concatenated records", func(t *testing.T) {
		nodes := []*Node{
			{ID: NodeID{0x01}, Addr: &net.UDPAddr{IP: net.IP{1, 2, 3, 4}, Port: 1000}},
			{ID: NodeID{0x02}, Addr: &net.UDPAddr{IP: net.IP{5, 6, 7, 8}, Port: 2000}},
		}
		got := EncodeNodes(nodes)
		assert.Len(t, got, 52)
	})
}

func TestDecodeNodes(t *testing.T) {
	t.Run("round-trips through EncodeNodes", func(t *testing.T) {
		var id NodeID
		id[0] = 0xCC
		original := []*Node{
			{ID: id, Addr: &net.UDPAddr{IP: net.IP{192, 168, 1, 1}, Port: 4321}},
		}
		nodes, err := DecodeNodes(EncodeNodes(original))
		require.NoError(t, err)
		require.Len(t, nodes, 1)
		assert.Equal(t, id, nodes[0].ID)
		assert.Equal(t, net.IP{192, 168, 1, 1}, nodes[0].Addr.IP)
		assert.Equal(t, 4321, nodes[0].Addr.Port)
	})

	t.Run("returns error for non-multiple-of-26 length", func(t *testing.T) {
		_, err := DecodeNodes("tooshort")
		assert.Error(t, err)
	})

	t.Run("empty string returns empty slice", func(t *testing.T) {
		nodes, err := DecodeNodes("")
		require.NoError(t, err)
		assert.Empty(t, nodes)
	})
}

func TestEncodePeer(t *testing.T) {
	t.Run("encodes to 6 bytes", func(t *testing.T) {
		got := EncodePeer(net.IP{1, 2, 3, 4}, 6881)
		assert.Len(t, got, 6)
	})

	t.Run("encodes known bytes correctly", func(t *testing.T) {
		got := []byte(EncodePeer(net.IP{10, 20, 30, 40}, 0x1234))
		assert.Equal(t, []byte{10, 20, 30, 40}, got[:4])
		assert.Equal(t, []byte{0x12, 0x34}, got[4:6])
	})
}

func TestDecodePeers(t *testing.T) {
	t.Run("round-trips through EncodePeer", func(t *testing.T) {
		ip := net.IP{1, 2, 3, 4}
		port := 6881
		addrs, err := DecodePeers([]string{EncodePeer(ip, port)})
		require.NoError(t, err)
		require.Len(t, addrs, 1)
		tcp := addrs[0].(*net.TCPAddr)
		assert.Equal(t, ip, tcp.IP)
		assert.Equal(t, port, tcp.Port)
	})

	t.Run("returns error for wrong record length", func(t *testing.T) {
		_, err := DecodePeers([]string{"short"})
		assert.Error(t, err)
	})

	t.Run("empty input returns empty slice", func(t *testing.T) {
		addrs, err := DecodePeers(nil)
		require.NoError(t, err)
		assert.Empty(t, addrs)
	})
}

func TestParseNodeID(t *testing.T) {
	t.Run("parses valid 20-byte string", func(t *testing.T) {
		s := "12345678901234567890"
		id, err := ParseNodeID(s)
		require.NoError(t, err)
		assert.Equal(t, s, string(id[:]))
	})

	t.Run("returns error for string shorter than 20 bytes", func(t *testing.T) {
		_, err := ParseNodeID("tooshort")
		assert.Error(t, err)
	})

	t.Run("returns error for string longer than 20 bytes", func(t *testing.T) {
		_, err := ParseNodeID("123456789012345678901")
		assert.Error(t, err)
	})
}
