package dht

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math/bits"
	"net"
	"time"
)

// NodeID is a 160-bit DHT node identifier.
type NodeID [20]byte

func (id NodeID) String() string {
	return hex.EncodeToString(id[:])
}

var ErrInvalidNodeID = errors.New("invalid node ID: must be 40 hex characters")

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

// Node represents a single DHT node.
type Node struct {
	ID           NodeID
	Addr         *net.UDPAddr
	LastSeen     time.Time
	FailureCount int
}

// IsGood reports whether the node is considered good per BEP-05:
// responded within goodNodeWindow and has no consecutive failures.
func (n *Node) IsGood(goodNodeWindow time.Duration) bool {
	return time.Since(n.LastSeen) < goodNodeWindow && n.FailureCount == 0
}

// IsQuestionable reports whether the node has not been contacted within
// goodNodeWindow but has not exceeded badFailureThreshold.
func (n *Node) IsQuestionable(goodNodeWindow time.Duration, badFailureThreshold int) bool {
	return time.Since(n.LastSeen) >= goodNodeWindow && n.FailureCount < badFailureThreshold
}

// IsBad reports whether the node has failed badFailureThreshold or more consecutive queries.
func (n *Node) IsBad(badFailureThreshold int) bool {
	return n.FailureCount >= badFailureThreshold
}
