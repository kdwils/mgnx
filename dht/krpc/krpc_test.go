package krpc_test

import (
	"testing"

	"github.com/kdwils/mgnx/dht/table"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseNodeID(t *testing.T) {
	t.Run("parses valid 20-byte string", func(t *testing.T) {
		s := "12345678901234567890"
		id, err := table.ParseNodeID(s)
		require.NoError(t, err)
		assert.Equal(t, s, string(id[:]))
	})

	t.Run("returns error for string shorter than 20 bytes", func(t *testing.T) {
		_, err := table.ParseNodeID("tooshort")
		assert.Error(t, err)
	})

	t.Run("returns error for string longer than 20 bytes", func(t *testing.T) {
		_, err := table.ParseNodeID("123456789012345678901")
		assert.Error(t, err)
	})
}
