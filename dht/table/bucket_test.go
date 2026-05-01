package table

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBucket_Contains(t *testing.T) {
	var min, max NodeID
	min[0] = 0x00
	max[0] = 0x80

	b := &Bucket{Min: min, Max: max}

	t.Run("id at min is contained", func(t *testing.T) {
		assert.True(t, b.Contains(min))
	})

	t.Run("id within range is contained", func(t *testing.T) {
		var id NodeID
		id[0] = 0x40
		assert.True(t, b.Contains(id))
	})

	t.Run("id at max is not contained (exclusive)", func(t *testing.T) {
		assert.False(t, b.Contains(max))
	})

	t.Run("id above max is not contained", func(t *testing.T) {
		var id NodeID
		id[0] = 0xFF
		assert.False(t, b.Contains(id))
	})
}

func TestBucket_IsFull(t *testing.T) {
	k := 8

	t.Run("empty bucket is not full", func(t *testing.T) {
		b := &Bucket{}
		assert.False(t, b.IsFull(k))
	})

	t.Run("bucket with k nodes is full", func(t *testing.T) {
		b := &Bucket{Nodes: make([]*Node, k)}
		assert.True(t, b.IsFull(k))
	})

	t.Run("bucket with fewer than k nodes is not full", func(t *testing.T) {
		b := &Bucket{Nodes: make([]*Node, k-1)}
		assert.False(t, b.IsFull(k))
	})
}

func TestBucket_IsStale(t *testing.T) {
	threshold := 15 * time.Minute

	t.Run("recently changed bucket is not stale", func(t *testing.T) {
		b := &Bucket{LastChanged: time.Now()}
		assert.False(t, b.IsStale(threshold))
	})

	t.Run("bucket unchanged beyond threshold is stale", func(t *testing.T) {
		b := &Bucket{LastChanged: time.Now().Add(-20 * time.Minute)}
		assert.True(t, b.IsStale(threshold))
	})

	t.Run("bucket unchanged exactly at threshold is stale", func(t *testing.T) {
		b := &Bucket{LastChanged: time.Now().Add(-threshold - time.Millisecond)}
		assert.True(t, b.IsStale(threshold))
	})
}
