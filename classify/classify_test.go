package classify_test

import (
	"testing"

	"github.com/kdwils/mgnx/classify"
	"github.com/kdwils/mgnx/db/gen"
	"github.com/stretchr/testify/assert"
)

const mb = 1024 * 1024
const gb = 1024 * mb

const testMinSize = 50 * mb
const testMaxSize = 150 * gb

// testAllowed mirrors the default production allowlist used in tests.
var testAllowed = map[string]struct{}{
	".mkv": {}, ".mp4": {}, ".avi": {}, ".mov": {}, ".wmv": {}, ".m4v": {},
	".ts": {}, ".m2ts": {}, ".vob": {}, ".flv": {}, ".webm": {},
	".srt": {}, ".ass": {}, ".ssa": {}, ".sub": {},
}

func video(n int, sizeEach int64) []classify.File {
	files := make([]classify.File, n)
	for i := range files {
		files[i] = classify.File{Path: "video.mkv", Size: sizeEach}
	}
	return files
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name      string
		torrent   string
		files     []classify.File
		totalSize int64
		want      classify.Result
	}{
		{
			name:      "standard movie",
			torrent:   "The.Dark.Knight.2008.1080p.BluRay.x264-GROUP",
			files:     video(1, 8*gb),
			totalSize: 8 * gb,
			want: classify.Result{
				State:        gen.TorrentStateClassified,
				ContentType:  gen.ContentTypeMovie,
				Title:        "The Dark Knight",
				Year:         2008,
				Quality:      "1080p",
				Encoding:     "x264",
				Source:       "BluRay",
				ReleaseGroup: "GROUP",
				SceneName:    "The.Dark.Knight.2008.1080p.BluRay.x264-GROUP",
			},
		},
		{
			name:      "standard TV SxxExx",
			torrent:   "Breaking.Bad.S01E07.720p.BluRay.x264-GROUP",
			files:     video(1, 1*gb),
			totalSize: 1 * gb,
			want: classify.Result{
				State:        gen.TorrentStateClassified,
				ContentType:  gen.ContentTypeTv,
				Title:        "Breaking Bad",
				Season:       1,
				Episode:      7,
				Quality:      "720p",
				Encoding:     "x264",
				Source:       "BluRay",
				ReleaseGroup: "GROUP",
				SceneName:    "Breaking.Bad.S01E07.720p.BluRay.x264-GROUP",
			},
		},
		{
			name:      "TV season pack",
			torrent:   "Game.of.Thrones.Season.3.1080p.BluRay.x264-GROUP",
			files:     video(10, 4*gb),
			totalSize: 40 * gb,
			want: classify.Result{
				State:        gen.TorrentStateClassified,
				ContentType:  gen.ContentTypeTv,
				Title:        "Game of Thrones",
				Season:       3,
				Quality:      "1080p",
				Encoding:     "x264",
				Source:       "BluRay",
				ReleaseGroup: "GROUP",
				SceneName:    "Game.of.Thrones.Season.3.1080p.BluRay.x264-GROUP",
			},
		},
		{
			name:      "TV alternate NxM format",
			torrent:   "Seinfeld.5x12.The.Stall.DVDRip.x264",
			files:     video(1, 500*mb),
			totalSize: 500 * mb,
			want: classify.Result{
				State:       gen.TorrentStateClassified,
				ContentType: gen.ContentTypeTv,
				Title:       "Seinfeld",
				Season:      5,
				Episode:     12,
				Encoding:    "x264",
				Source:      "DVDRip",
				SceneName:   "Seinfeld.5x12.The.Stall.DVDRip.x264",
			},
		},
		{
			name:      "TV complete series",
			torrent:   "The.Wire.Complete.Series.720p.HDTV.x264-GROUP",
			files:     video(60, 1*gb),
			totalSize: 60 * gb,
			want: classify.Result{
				State:        gen.TorrentStateClassified,
				ContentType:  gen.ContentTypeTv,
				Title:        "The Wire",
				Quality:      "720p",
				Encoding:     "x264",
				Source:       "HDTV",
				ReleaseGroup: "GROUP",
				SceneName:    "The.Wire.Complete.Series.720p.HDTV.x264-GROUP",
			},
		},
		{
			name:      "4K HDR movie",
			torrent:   "Avengers.Endgame.2019.2160p.UHD.BluRay.HDR.x265-GROUP",
			files:     video(1, 60*gb),
			totalSize: 60 * gb,
			want: classify.Result{
				State:        gen.TorrentStateClassified,
				ContentType:  gen.ContentTypeMovie,
				Title:        "Avengers Endgame",
				Year:         2019,
				Quality:      "2160p",
				Encoding:     "x265",
				DynamicRange: "HDR",
				Source:       "BluRay",
				ReleaseGroup: "GROUP",
				SceneName:    "Avengers.Endgame.2019.2160p.UHD.BluRay.HDR.x265-GROUP",
			},
		},
		{
			name:      "WEB-DL TV episode",
			torrent:   "Succession.S04E03.1080p.WEB-DL.H.264-GROUP",
			files:     video(1, 3*gb),
			totalSize: 3 * gb,
			want: classify.Result{
				State:        gen.TorrentStateClassified,
				ContentType:  gen.ContentTypeTv,
				Title:        "Succession",
				Season:       4,
				Episode:      3,
				Quality:      "1080p",
				Source:       "WEB-DL",
				ReleaseGroup: "GROUP",
				SceneName:    "Succession.S04E03.1080p.WEB-DL.H.264-GROUP",
			},
		},
		{
			name:      "single-file torrent with video extension",
			torrent:   "Interstellar.2014.1080p.BluRay.x264-GROUP.mkv",
			files:     video(1, 10*gb),
			totalSize: 10 * gb,
			want: classify.Result{
				State:        gen.TorrentStateClassified,
				ContentType:  gen.ContentTypeMovie,
				Title:        "Interstellar",
				Year:         2014,
				Quality:      "1080p",
				Encoding:     "x264",
				Source:       "BluRay",
				ReleaseGroup: "GROUP",
				SceneName:    "Interstellar.2014.1080p.BluRay.x264-GROUP.mkv",
			},
		},
		{
			name:      "HDR10 dynamic range",
			torrent:   "Dune.2021.2160p.BluRay.HDR10.x265-GROUP",
			files:     video(1, 60*gb),
			totalSize: 60 * gb,
			want: classify.Result{
				State:        gen.TorrentStateClassified,
				ContentType:  gen.ContentTypeMovie,
				Title:        "Dune",
				Year:         2021,
				Quality:      "2160p",
				Encoding:     "x265",
				DynamicRange: "HDR10",
				Source:       "BluRay",
				ReleaseGroup: "GROUP",
				SceneName:    "Dune.2021.2160p.BluRay.HDR10.x265-GROUP",
			},
		},
		{
			name:      "underscore-separated name",
			torrent:   "The_Matrix_1999_1080p_BluRay_x264-GROUP",
			files:     video(1, 10*gb),
			totalSize: 10 * gb,
			want: classify.Result{
				State:        gen.TorrentStateClassified,
				ContentType:  gen.ContentTypeMovie,
				Title:        "The Matrix",
				Year:         1999,
				Quality:      "1080p",
				Encoding:     "x264",
				Source:       "BluRay",
				ReleaseGroup: "GROUP",
				SceneName:    "The_Matrix_1999_1080p_BluRay_x264-GROUP",
			},
		},
		{
			name:      "classified - no year or TV markers but has video",
			torrent:   "Some.Random.Content.HDTV.x264",
			files:     video(1, 500*mb),
			totalSize: 500 * mb,
			want: classify.Result{
				State:       gen.TorrentStateClassified,
				ContentType: gen.ContentTypeUnknown,
				Title:       "Some Random Content",
				Encoding:    "x264",
				Source:      "HDTV",
				SceneName:   "Some.Random.Content.HDTV.x264",
			},
		},
		{
			name:      "rejected - no video files",
			torrent:   "Movie.2020.1080p.BluRay.x264-GROUP",
			files:     []classify.File{{Path: "readme.txt", Size: 1 * mb}},
			totalSize: 100 * mb,
			want: classify.Result{
				State:        gen.TorrentStateRejected,
				ContentType:  gen.ContentTypeMovie,
				Title:        "Movie",
				Year:         2020,
				Quality:      "1080p",
				Encoding:     "x264",
				Source:       "BluRay",
				ReleaseGroup: "GROUP",
				SceneName:    "Movie.2020.1080p.BluRay.x264-GROUP",
			},
		},
		{
			name:      "rejected - too small",
			torrent:   "Movie.2020.1080p.BluRay.x264-GROUP",
			files:     video(1, 10*mb),
			totalSize: 10 * mb,
			want: classify.Result{
				State:        gen.TorrentStateRejected,
				ContentType:  gen.ContentTypeMovie,
				Title:        "Movie",
				Year:         2020,
				Quality:      "1080p",
				Encoding:     "x264",
				Source:       "BluRay",
				ReleaseGroup: "GROUP",
				SceneName:    "Movie.2020.1080p.BluRay.x264-GROUP",
			},
		},
		{
			name:      "rejected - too large",
			torrent:   "Movie.2020.1080p.BluRay.x264-GROUP",
			files:     video(1, 200*gb),
			totalSize: 200 * gb,
			want: classify.Result{
				State:        gen.TorrentStateRejected,
				ContentType:  gen.ContentTypeMovie,
				Title:        "Movie",
				Year:         2020,
				Quality:      "1080p",
				Encoding:     "x264",
				Source:       "BluRay",
				ReleaseGroup: "GROUP",
				SceneName:    "Movie.2020.1080p.BluRay.x264-GROUP",
			},
		},
		{
			name:    "rejected - dominant bad extensions",
			torrent: "Some.Pack.2020.1080p.BluRay.x264-GROUP",
			files: []classify.File{
				{Path: "file.mkv", Size: 1 * gb},
				{Path: "a.zip", Size: 100 * mb},
				{Path: "b.rar", Size: 100 * mb},
				{Path: "c.exe", Size: 100 * mb},
			},
			totalSize: 1300 * mb,
			want: classify.Result{
				State:        gen.TorrentStateRejected,
				ContentType:  gen.ContentTypeMovie,
				Title:        "Some Pack",
				Year:         2020,
				Quality:      "1080p",
				Encoding:     "x264",
				Source:       "BluRay",
				ReleaseGroup: "GROUP",
				SceneName:    "Some.Pack.2020.1080p.BluRay.x264-GROUP",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classify.Classify(tc.torrent, tc.files, tc.totalSize, testMinSize, testMaxSize, testAllowed)
			assert.Equal(t, tc.want, got)
		})
	}
}
