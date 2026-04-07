package dht

import (
	"encoding/binary"
	"fmt"
	"net"
)

// Msg is the top-level KRPC message envelope.
type Msg struct {
	T  string   `bencode:"t"`
	Y  string   `bencode:"y"`
	Q  string   `bencode:"q,omitempty"`
	A  *MsgArgs `bencode:"a,omitempty"`
	R  *Return  `bencode:"r,omitempty"`
	E  []any    `bencode:"e,omitempty"`
	V  string   `bencode:"v,omitempty"`
	IP string   `bencode:"ip,omitempty"` // BEP-42: our external IP as seen by the responder (4 bytes IPv4)
}

// MsgArgs holds query arguments (union of fields across all query types).
type MsgArgs struct {
	ID          string `bencode:"id"`
	Target      string `bencode:"target,omitempty"`
	InfoHash    string `bencode:"info_hash,omitempty"`
	Token       string `bencode:"token,omitempty"`
	Port        int    `bencode:"port,omitempty"`
	ImpliedPort *int   `bencode:"implied_port,omitempty"`
}

// Return holds the response body (union of fields across all response types).
type Return struct {
	ID       string   `bencode:"id"`
	Nodes    string   `bencode:"nodes,omitempty"`
	Values   []string `bencode:"values,omitempty"`
	Token    string   `bencode:"token,omitempty"`
	Samples  string   `bencode:"samples,omitempty"`
	Interval int      `bencode:"interval,omitempty"`
	Num      int      `bencode:"num,omitempty"`
}

// KRPC error codes per BEP-05 §KRPC Protocol and BEP-51 §3.
const (
	ErrGeneric  = 201
	ErrServer   = 202
	ErrProtocol = 203
	ErrMethod   = 204 // "Method Unknown" — returned by nodes that don't implement a query (e.g. sample_infohashes per BEP-51 §3)
)

// isMethodUnknown reports whether msg is a KRPC 204 "method unknown" error.
// Per BEP-51 §3, nodes that don't implement sample_infohashes return this code.
func isMethodUnknown(msg *Msg) bool {
	if msg == nil || len(msg.E) < 1 {
		return false
	}
	code, ok := msg.E[0].(int64)
	return ok && code == ErrMethod
}

// EncodeNodes encodes a slice of nodes as a concatenated sequence of 26-byte
// compact node records per BEP-05 §Compact Node Info.
// Non-IPv4 nodes are silently skipped.
// Format per node: [20]byte ID | [4]byte IPv4 big-endian | [2]byte port big-endian.
func EncodeNodes(nodes []*Node) string {
	buf := make([]byte, 0, len(nodes)*26)
	for _, n := range nodes {
		ip := n.Addr.IP.To4()
		if ip == nil {
			continue
		}
		var record [26]byte
		copy(record[:20], n.ID[:])
		copy(record[20:24], ip)
		binary.BigEndian.PutUint16(record[24:26], uint16(n.Addr.Port))
		buf = append(buf, record[:]...)
	}
	return string(buf)
}

// DecodeNodes parses a compact node string into a slice of nodes per
// BEP-05 §Compact Node Info.
// Returns an error if the length is not a multiple of 26.
func DecodeNodes(s string) ([]*Node, error) {
	if len(s)%26 != 0 {
		return nil, fmt.Errorf("dht: nodes length %d is not a multiple of 26", len(s))
	}
	nodes := make([]*Node, len(s)/26)
	for i := range nodes {
		off := i * 26
		var id NodeID
		copy(id[:], s[off:off+20])
		ip := make(net.IP, 4)
		copy(ip, s[off+20:off+24])
		port := int(binary.BigEndian.Uint16([]byte(s[off+24 : off+26])))
		nodes[i] = &Node{
			ID:   id,
			Addr: &net.UDPAddr{IP: ip, Port: port},
		}
	}
	return nodes, nil
}

// EncodePeer encodes a single peer as a 6-byte compact peer record per
// BEP-05 §Compact Peer Info.
// Format: [4]byte IPv4 big-endian | [2]byte port big-endian.
func EncodePeer(ip net.IP, port int) string {
	var buf [6]byte
	copy(buf[:4], ip.To4())
	binary.BigEndian.PutUint16(buf[4:6], uint16(port))
	return string(buf[:])
}

// DecodePeers parses a list of 6-byte compact peer strings into net.Addr values.
// Per BEP-05, these are TCP peer addresses for BitTorrent protocol, not DHT UDP nodes.
// Returns an error if any entry is not exactly 6 bytes.
func DecodePeers(values []string) ([]net.Addr, error) {
	addrs := make([]net.Addr, 0, len(values))
	for _, v := range values {
		if len(v) != 6 {
			return nil, fmt.Errorf("dht: peer record must be 6 bytes, got %d", len(v))
		}
		ip := make(net.IP, 4)
		copy(ip, v[:4])
		port := int(binary.BigEndian.Uint16([]byte(v[4:6])))
		addrs = append(addrs, &net.TCPAddr{IP: ip, Port: port})
	}
	return addrs, nil
}

// ParseNodeID parses a raw 20-byte string into a NodeID.
// Returns an error if s is not exactly 20 bytes.
func ParseNodeID(s string) (NodeID, error) {
	if len(s) != 20 {
		return NodeID{}, fmt.Errorf("dht: node ID must be 20 bytes, got %d", len(s))
	}
	var id NodeID
	copy(id[:], s)
	return id, nil
}
