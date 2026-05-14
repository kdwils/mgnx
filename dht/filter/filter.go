package filter

import (
	"context"
	"sync"
	"time"

	"github.com/bits-and-blooms/bloom/v3"
)

// BloomFilter is a tiered filter for infohash deduplication.
// It maintains two bloom filters (active and previous) so that rotation
// does not immediately cause re-discovery of recently-seen infohashes.
type BloomFilter struct {
	active   *bloom.BloomFilter
	previous *bloom.BloomFilter
	bloomN   uint
	bloomP   float64
	rotation time.Duration
	mu       sync.Mutex
}

func NewBloomFilter(bloomN uint, bloomP float64, rotation time.Duration) *BloomFilter {
	bf := &BloomFilter{
		active:   bloom.NewWithEstimates(bloomN, bloomP),
		previous: bloom.NewWithEstimates(bloomN, bloomP),
		rotation: rotation,
		bloomN:   bloomN,
		bloomP:   bloomP,
	}
	return bf
}

func (b *BloomFilter) SeenOrAdd(h [20]byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active.Test(h[:]) || b.previous.Test(h[:]) {
		return true
	}
	b.active.Add(h[:])
	return false
}

func (b *BloomFilter) Rotate(ctx context.Context) {
	ticker := time.NewTicker(b.rotation)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			b.mu.Lock()
			b.previous = b.active
			b.active = bloom.NewWithEstimates(b.bloomN, b.bloomP)
			b.mu.Unlock()
		case <-ctx.Done():
			return
		}
	}
}
