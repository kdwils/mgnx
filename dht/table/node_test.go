package table

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseNodeID(t *testing.T) {
	t.Run("parses valid 20-byte string", func(t *testing.T) {
		s := "12345678901234567890"
		id, err := ParseNodeID(s)
		require.NoError(t, err)
		assert.Equal(t, s, string(id[:]))
	})

	t.Run("returns error for string shorter than 20 bytes", func(t *testing.T) {
		_, err := ParseNodeID("tooshort")
		assert.Error(t, err)
	})

	t.Run("returns error for string longer than 20 bytes", func(t *testing.T) {
		_, err := ParseNodeID("123456789012345678901")
		assert.Error(t, err)
	})
}

func TestNodeID_XOR(t *testing.T) {
	t.Run("basic xor", func(t *testing.T) {
		var a, b NodeID
		a[0] = 0xFF
		b[0] = 0x0F
		want := NodeID{}
		want[0] = 0xF0
		assert.Equal(t, want, a.XOR(b))
	})

	t.Run("xor with self is zero", func(t *testing.T) {
		var a NodeID
		a[5] = 0xAB
		assert.Equal(t, NodeID{}, a.XOR(a))
	})

	t.Run("xor is commutative", func(t *testing.T) {
		var a, b NodeID
		a[2] = 0x3C
		b[2] = 0x55
		assert.Equal(t, a.XOR(b), b.XOR(a))
	})
}

func TestNodeID_CommonPrefixLen(t *testing.T) {
	t.Run("identical nodes share 160 bits", func(t *testing.T) {
		var a NodeID
		a[3] = 0x12
		assert.Equal(t, 160, a.CommonPrefixLen(a))
	})

	t.Run("first bits differ", func(t *testing.T) {
		var a, b NodeID
		a[0] = 0x80 // 1000 0000
		b[0] = 0x40 // 0100 0000  XOR = 1100 0000, leading zeros = 0
		assert.Equal(t, 0, a.CommonPrefixLen(b))
	})

	t.Run("first byte equal second byte differs", func(t *testing.T) {
		var a, b NodeID
		a[1] = 0x80 // XOR byte 0 = 0x00, byte 1 = 0xC0 → 8 leading zeros total
		b[1] = 0x40
		assert.Equal(t, 8, a.CommonPrefixLen(b))
	})
}

func TestNode_IsGood(t *testing.T) {
	window := 15 * time.Minute

	t.Run("recently seen with no failures is good", func(t *testing.T) {
		n := &Node{LastSeen: time.Now(), FailureCount: 0}
		assert.True(t, n.IsGood(window))
	})

	t.Run("recently seen with failures is not good", func(t *testing.T) {
		n := &Node{LastSeen: time.Now(), FailureCount: 1}
		assert.False(t, n.IsGood(window))
	})

	t.Run("stale with no failures is not good", func(t *testing.T) {
		n := &Node{LastSeen: time.Now().Add(-20 * time.Minute), FailureCount: 0}
		assert.False(t, n.IsGood(window))
	})
}

func TestNode_IsQuestionable(t *testing.T) {
	window := 15 * time.Minute
	threshold := 2

	t.Run("stale with failures below threshold is questionable", func(t *testing.T) {
		n := &Node{LastSeen: time.Now().Add(-20 * time.Minute), FailureCount: 1}
		assert.True(t, n.IsQuestionable(window, threshold))
	})

	t.Run("recent node is not questionable", func(t *testing.T) {
		n := &Node{LastSeen: time.Now(), FailureCount: 0}
		assert.False(t, n.IsQuestionable(window, threshold))
	})

	t.Run("stale node at failure threshold is not questionable", func(t *testing.T) {
		n := &Node{LastSeen: time.Now().Add(-20 * time.Minute), FailureCount: 2}
		assert.False(t, n.IsQuestionable(window, threshold))
	})
}

func TestNode_IsBad(t *testing.T) {
	threshold := 2

	t.Run("failure count at threshold is bad", func(t *testing.T) {
		n := &Node{FailureCount: 2}
		assert.True(t, n.IsBad(threshold))
	})

	t.Run("failure count above threshold is bad", func(t *testing.T) {
		n := &Node{FailureCount: 5}
		assert.True(t, n.IsBad(threshold))
	})

	t.Run("failure count below threshold is not bad", func(t *testing.T) {
		n := &Node{FailureCount: 1}
		assert.False(t, n.IsBad(threshold))
	})
}
