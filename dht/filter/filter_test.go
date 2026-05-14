package filter

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewBloomFilter(t *testing.T) {
	bf := NewBloomFilter(10000, 0.001, 5*time.Minute)

	assert.NotNil(t, bf.active)
	assert.NotNil(t, bf.previous)
	assert.Equal(t, uint(10000), bf.bloomN)
	assert.Equal(t, 0.001, bf.bloomP)
	assert.Equal(t, 5*time.Minute, bf.rotation)

	var h [20]byte
	h[0] = 0xAB
	assert.False(t, bf.SeenOrAdd(h), "new item should return false")
	assert.True(t, bf.SeenOrAdd(h), "duplicate item should return true")
}

func TestBloomFilter_SeenOrAdd(t *testing.T) {
	t.Run("new items return false and duplicates return true", func(t *testing.T) {
		bf := NewBloomFilter(10000, 0.001, time.Hour)

		for i := range 100 {
			var h [20]byte
			h[0] = byte(i)
			assert.False(t, bf.SeenOrAdd(h), "item %d should be new", i)
		}

		for i := range 100 {
			var h [20]byte
			h[0] = byte(i)
			assert.True(t, bf.SeenOrAdd(h), "item %d should be duplicate", i)
		}
	})

	t.Run("tiered detection after rotation", func(t *testing.T) {
		bf := NewBloomFilter(10000, 0.001, 50*time.Millisecond)

		var h [20]byte
		h[0] = 0xCD
		assert.False(t, bf.SeenOrAdd(h))

		time.Sleep(150 * time.Millisecond)

		assert.True(t, bf.SeenOrAdd(h), "item from previous filter should still be detected")

		var h2 [20]byte
		h2[0] = 0x99
		assert.False(t, bf.SeenOrAdd(h2), "brand new item should not be detected")
	})

	t.Run("concurrent access is safe", func(t *testing.T) {
		bf := NewBloomFilter(10000, 0.001, time.Hour)

		var wg sync.WaitGroup
		for i := range 200 {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				var h [20]byte
				h[0] = byte(idx % 50)
				h[1] = 0xBB
				bf.SeenOrAdd(h)
			}(i)
		}
		wg.Wait()

		for i := range 50 {
			var h [20]byte
			h[0] = byte(i)
			h[1] = 0xBB
			assert.True(t, bf.SeenOrAdd(h))
		}
	})
}

func TestBloomFilter_Rotate(t *testing.T) {
	t.Run("rotates active to previous on ticker", func(t *testing.T) {
		bf := NewBloomFilter(10000, 0.001, 50*time.Millisecond)

		var h [20]byte
		h[0] = 0xEF
		assert.False(t, bf.SeenOrAdd(h))

		time.Sleep(150 * time.Millisecond)

		assert.True(t, bf.SeenOrAdd(h), "item should be detected via previous filter")
	})

	t.Run("stops on context cancellation", func(t *testing.T) {
		bf := NewBloomFilter(10000, 0.001, 10*time.Millisecond)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})

		go func() {
			bf.Rotate(ctx)
			close(done)
		}()

		time.Sleep(20 * time.Millisecond)
		cancel()

		select {
		case <-done:
		case <-time.After(1 * time.Second):
			t.Fatal("Rotate did not exit after context cancellation")
		}
	})

	t.Run("concurrent rotate and seenOrAdd", func(t *testing.T) {
		bf := NewBloomFilter(10000, 0.001, 10*time.Millisecond)

		ctx := t.Context()
		go bf.Rotate(ctx)

		var wg sync.WaitGroup
		for i := range 200 {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				var h [20]byte
				h[0] = byte(idx % 50)
				h[1] = 0xBB
				bf.SeenOrAdd(h)
			}(i)
		}
		wg.Wait()
	})
}
