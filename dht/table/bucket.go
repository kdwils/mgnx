package table

import (
	"bytes"
	"crypto/rand"
	"time"
)

type Bucket struct {
	Min              NodeID    `json:"min"`
	Max              NodeID    `json:"max"`
	Depth            int       `json:"depth"`
	Nodes            []*Node   `json:"nodes"`
	ReplacementCache []*Node   `json:"replacement_cache"`
	LastChanged      time.Time `json:"last_changed"`
}

func (b *Bucket) Contains(id NodeID) bool {
	if b.Max == (NodeID{}) {
		return bytes.Compare(id[:], b.Min[:]) >= 0
	}
	return bytes.Compare(id[:], b.Min[:]) >= 0 && bytes.Compare(id[:], b.Max[:]) < 0
}

func (b *Bucket) IsFull(k int) bool {
	return len(b.Nodes) == k
}

func (b *Bucket) IsStale(staleThreshold time.Duration) bool {
	return time.Since(b.LastChanged) > staleThreshold
}

func (b *Bucket) GetRandomNodeID() NodeID {
	var id NodeID
	rand.Read(id[:])
	byteIdx := b.Depth / 8
	bitIdx := 7 - (b.Depth % 8)
	for i := range byteIdx {
		id[i] = b.Min[i]
	}
	prefixMask := byte(0xFF) << (bitIdx + 1)
	id[byteIdx] = (b.Min[byteIdx] & prefixMask) | (id[byteIdx] &^ prefixMask)
	return id
}
