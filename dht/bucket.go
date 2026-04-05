package dht

import (
	"bytes"
	"time"
)

// Bucket holds up to k nodes for a contiguous range of the NodeID keyspace.
// Depth is the number of prefix bits that define this bucket's range; used to
// compute the split point without 160-bit arithmetic.
// Max == NodeID{} is a sentinel meaning the bucket extends to the end of the
// keyspace (exclusive upper bound = 2^160).
type Bucket struct {
	Min              NodeID  // inclusive lower bound
	Max              NodeID  // exclusive upper bound; NodeID{} means 2^160
	Depth            int     // number of prefix bits defining this bucket's range
	Nodes            []*Node // up to k, ordered by LastSeen ascending
	ReplacementCache []*Node // overflow queue, up to k
	LastChanged      time.Time
}

// Contains reports whether id falls within [Min, Max).
// When Max == NodeID{} the bucket has no upper bound (covers [Min, 2^160)).
func (b *Bucket) Contains(id NodeID) bool {
	if b.Max == (NodeID{}) {
		return bytes.Compare(id[:], b.Min[:]) >= 0
	}
	return bytes.Compare(id[:], b.Min[:]) >= 0 && bytes.Compare(id[:], b.Max[:]) < 0
}

// IsFull reports whether the bucket has reached capacity k.
func (b *Bucket) IsFull(k int) bool {
	return len(b.Nodes) == k
}

// IsStale reports whether the bucket has had no activity within staleThreshold.
func (b *Bucket) IsStale(staleThreshold time.Duration) bool {
	return time.Since(b.LastChanged) > staleThreshold
}
