package table

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"math/bits"
	"net"
)

// NodeID is a 160-bit DHT node identifier.
type NodeID [20]byte

func (id NodeID) String() string { return hex.EncodeToString(id[:]) }

// XOR returns the XOR distance between two node IDs.
func (id NodeID) XOR(other NodeID) NodeID {
	var result NodeID
	for i := range id {
		result[i] = id[i] ^ other[i]
	}
	return result
}

// CommonPrefixLen returns the number of leading bits shared between two node IDs.
func (id NodeID) CommonPrefixLen(other NodeID) int {
	xor := id.XOR(other)
	for i, b := range xor {
		if b != 0 {
			return i*8 + bits.LeadingZeros8(b)
		}
	}
	return 160
}

// Less reports whether id is less than other (lexicographic byte order).
func (id NodeID) Less(other NodeID) bool {
	return bytes.Compare(id[:], other[:]) < 0
}

var ErrInvalidNodeID = errors.New("invalid node ID: must be 40 hex characters")

// ParseNodeIDHex parses a 40-character hex string into a NodeID.
func ParseNodeIDHex(s string) (NodeID, error) {
	if len(s) != 40 {
		return NodeID{}, ErrInvalidNodeID
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return NodeID{}, ErrInvalidNodeID
	}
	var id NodeID
	copy(id[:], b)
	return id, nil
}

// ParseNodeID parses a raw 20-byte string into a NodeID.
// Returns an error if s is not exactly 20 bytes.
func ParseNodeID(s string) (NodeID, error) {
	if len(s) != 20 {
		return NodeID{}, fmt.Errorf("table: node ID must be 20 bytes, got %d", len(s))
	}
	var id NodeID
	copy(id[:], s)
	return id, nil
}

// DeriveNodeIDFromIP generates a BEP-42 compliant node ID for the given IPv4
// address. Implements BEP-42 (DHT Security Extension) §Node ID.
func DeriveNodeIDFromIP(ip net.IP) (NodeID, error) {
	var id NodeID

	ip4 := ip.To4()
	if ip4 == nil {
		return id, nil
	}

	var rBuf [1]byte
	rand.Read(rBuf[:])
	r := rBuf[0] & 0x07

	ipUint32 := binary.BigEndian.Uint32(ip4)
	seed := (ipUint32 & 0x030f3fff) | (uint32(r) << 29)

	var seedBuf [4]byte
	binary.BigEndian.PutUint32(seedBuf[:], seed)

	table := crc32.MakeTable(crc32.Castagnoli)
	crc := crc32.Checksum(seedBuf[:], table)

	rand.Read(id[:])

	id[0] = byte(crc >> 24)
	id[1] = byte(crc >> 16)
	id[2] = (byte(crc>>8) & 0xf8) | (id[2] & 0x07)
	id[19] = (id[19] & 0xf8) | r

	return id, nil
}

func ValidateNodeIDForIP(ip net.IP, id NodeID) error {
	ip4 := ip.To4()
	if ip4 == nil {
		return errors.New("not an IPv4 address")
	}

	r := id[19] & 0x07
	seed := (binary.BigEndian.Uint32(ip4) & 0x030f3fff) | (uint32(r) << 29)
	var seedBuf [4]byte
	binary.BigEndian.PutUint32(seedBuf[:], seed)
	crc := crc32.Checksum(seedBuf[:], crc32.MakeTable(crc32.Castagnoli))

	if byte(crc>>24) != id[0] {
		return fmt.Errorf("id[0] (%02x) does not match crc[0] (%02x)", id[0], byte(crc>>24))
	}
	if byte(crc>>16) != id[1] {
		return fmt.Errorf("id[1] (%02x) does not match crc[1] (%02x)", id[1], byte(crc>>16))
	}
	if byte(crc>>8)&0xf8 != id[2]&0xf8 {
		return fmt.Errorf("id[2] top 5 bits (%02x) do not match crc bits (%02x)", id[2]&0xf8, byte(crc>>8)&0xf8)
	}
	return nil
}
