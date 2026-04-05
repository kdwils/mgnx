package metadata

import (
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeInfoSingleFile(t *testing.T) {
	data, err := bencode.Marshal(map[string]interface{}{
		"name":         "ubuntu.iso",
		"length":       int64(1024 * 1024 * 700),
		"piece length": int64(512 * 1024),
		"pieces":       "xxxxxxxxxxxxxxxxxxxx", // 20 bytes placeholder
	})
	require.NoError(t, err)

	info, err := decodeInfo(data)
	require.NoError(t, err)

	assert.Equal(t, "ubuntu.iso", info.Name)
	assert.Equal(t, int64(1024*1024*700), info.TotalSize)
	assert.Len(t, info.Files, 1)
	assert.Equal(t, "ubuntu.iso", info.Files[0].Path)
	assert.Equal(t, int64(1024*1024*700), info.Files[0].Size)
}

func TestDecodeInfoMultiFile(t *testing.T) {
	data, err := bencode.Marshal(map[string]interface{}{
		"name":         "My.Show.S01",
		"piece length": int64(512 * 1024),
		"pieces":       "xxxxxxxxxxxxxxxxxxxx",
		"files": []interface{}{
			map[string]interface{}{
				"path":   []string{"episode1.mkv"},
				"length": int64(500 * 1024 * 1024),
			},
			map[string]interface{}{
				"path":   []string{"subs", "episode1.srt"},
				"length": int64(50 * 1024),
			},
		},
	})
	require.NoError(t, err)

	info, err := decodeInfo(data)
	require.NoError(t, err)

	assert.Equal(t, "My.Show.S01", info.Name)
	assert.Equal(t, int64(500*1024*1024+50*1024), info.TotalSize)
	assert.Len(t, info.Files, 2)
	assert.Equal(t, "episode1.mkv", info.Files[0].Path)
	assert.Equal(t, int64(500*1024*1024), info.Files[0].Size)
	assert.Equal(t, "subs/episode1.srt", info.Files[1].Path)
	assert.Equal(t, int64(50*1024), info.Files[1].Size)
}
