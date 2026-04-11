package dht

import (
	"sync"
	"time"

	"github.com/bits-and-blooms/bloom/v3"
)

const (
	bloomN        = 10_000_000
	bloomP        = 0.001
	bloomRotation = 5 * time.Minute
)

// BloomFilter is a double-rotating bloom filter for infohash deduplication.
type BloomFilter struct {
	active   *bloom.BloomFilter
	previous *bloom.BloomFilter
	mu       sync.Mutex
	rotateAt time.Time
}

// NewBloomFilter creates a BloomFilter with n=1,000,000 and p=0.001.
func NewBloomFilter() *BloomFilter {
	return &BloomFilter{
		active:   bloom.NewWithEstimates(bloomN, bloomP),
		previous: bloom.NewWithEstimates(bloomN, bloomP),
		rotateAt: time.Now().Add(bloomRotation),
	}
}

// SeenOrAdd returns true if h was already seen (in either filter).
// If not seen, it adds h to the active filter and returns false.
func (b *BloomFilter) SeenOrAdd(h [20]byte) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if time.Now().After(b.rotateAt) {
		b.previous = b.active
		b.active = bloom.NewWithEstimates(bloomN, bloomP)
		b.rotateAt = time.Now().Add(bloomRotation)
	}

	if b.active.Test(h[:]) || b.previous.Test(h[:]) {
		return true
	}

	b.active.Add(h[:])
	return false
}

func (b *BloomFilter) StartRotator() {
	go func() {
		ticker := time.NewTicker(bloomRotation)
		defer ticker.Stop()

		for range ticker.C {
			b.mu.Lock()
			b.previous = b.active
			b.active = bloom.NewWithEstimates(bloomN, bloomP)
			b.mu.Unlock()
		}
	}()
}
