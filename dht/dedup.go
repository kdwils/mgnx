package dht

import (
	"context"
	"sync"
	"time"

	"github.com/bits-and-blooms/bloom/v3"
)

// BloomFilter is a filter for infohash deduplication.
type BloomFilter struct {
	active   *bloom.BloomFilter
	bloomN   uint
	bloomP   float64
	rotation time.Duration
	mu       sync.RWMutex
}

func NewBloomFilter(bloomN uint, bloomP float64, rotation time.Duration) *BloomFilter {
	bf := &BloomFilter{
		active:   bloom.NewWithEstimates(bloomN, bloomP),
		rotation: rotation,
		bloomN:   bloomN,
		bloomP:   bloomP,
	}
	return bf
}

func (b *BloomFilter) SeenOrAdd(h [20]byte) bool {
	b.mu.RLock()
	seen := b.active.Test(h[:])
	b.mu.RUnlock()

	if seen {
		return true
	}

	b.mu.Lock()
	b.active.Add(h[:])
	b.mu.Unlock()

	return false
}

func (b *BloomFilter) Rotate(ctx context.Context) {
	ticker := time.NewTicker(b.rotation)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			b.mu.Lock()
			b.active = bloom.NewWithEstimates(b.bloomN, b.bloomP)
			b.mu.Unlock()
		case <-ctx.Done():
			return
		}
	}
}
