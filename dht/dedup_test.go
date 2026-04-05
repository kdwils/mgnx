package dht

import (
	"crypto/rand"
	"testing"

	"github.com/bits-and-blooms/bloom/v3"
)

func TestBloomFilter_SeenOrAdd(t *testing.T) {
	bf := NewBloomFilter()

	var h [20]byte
	if _, err := rand.Read(h[:]); err != nil {
		t.Fatal(err)
	}

	if bf.SeenOrAdd(h) {
		t.Fatal("expected false on first call")
	}
	if !bf.SeenOrAdd(h) {
		t.Fatal("expected true on second call")
	}
}

func TestBloomFilter_PreviousWindowVisible(t *testing.T) {
	bf := NewBloomFilter()

	var h [20]byte
	if _, err := rand.Read(h[:]); err != nil {
		t.Fatal(err)
	}
	bf.SeenOrAdd(h)

	// Manually rotate: simulate the rotation by backdating rotateAt.
	bf.mu.Lock()
	bf.previous = bf.active
	bf.active = bloom.NewWithEstimates(bloomN, bloomP)
	bf.mu.Unlock()

	// h is now in previous; SeenOrAdd should still detect it.
	if !bf.SeenOrAdd(h) {
		t.Fatal("expected true: hash should be visible in previous filter")
	}
}

func TestBloomFilter_FalsePositiveRate(t *testing.T) {
	bf := NewBloomFilter()

	// Insert 1,000,000 items.
	for i := range 1_000_000 {
		var h [20]byte
		h[0] = byte(i)
		h[1] = byte(i >> 8)
		h[2] = byte(i >> 16)
		bf.SeenOrAdd(h)
	}

	// Test 10,000 unseen items and count false positives.
	falsePositives := 0
	trials := 10_000
	for i := range trials {
		var h [20]byte
		h[0] = byte(i)
		h[1] = byte(i >> 8)
		h[2] = byte(i >> 16)
		h[19] = 0xff // distinguishes from inserted items
		if bf.SeenOrAdd(h) {
			falsePositives++
		}
	}

	rate := float64(falsePositives) / float64(trials)
	// Allow up to 0.5% — 5x the target p=0.001, given hash overlap risk in the test.
	if rate > 0.005 {
		t.Errorf("false positive rate %.4f exceeds threshold 0.005", rate)
	}
}
