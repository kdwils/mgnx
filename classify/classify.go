package classify

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/kdwils/mgnx/db/gen"
)

// File is a file entry from torrent metadata.
type File struct {
	Path string
	Size int64
}

// Result is the output of classifying a torrent.
type Result struct {
	State        gen.TorrentState
	ContentType  gen.ContentType
	Title        string
	Year         int
	Season       int
	Episode      int
	Quality      string
	Encoding     string
	DynamicRange string
	Source       string
	ReleaseGroup string
	SceneName    string
}

type tvInfo struct {
	isTV    bool
	season  int
	episode int
}

var (
	// TV/Season patterns.
	// seasonPackPattern requires an explicit S##/Season ## marker; "Complete" alone
	// is not sufficient to avoid false-positives on non-TV names.
	seasonPackPattern = regexp.MustCompile(`(?i)\bS(?:eason)?[\s._-]?\d{1,2}\b`)

	// episodePattern is used only for title-cut detection.
	// The multi-episode suffix (?:[E-]\d{1,2})* handles S01E01E02 and S01E01-E03.
	episodePattern = regexp.MustCompile(`(?i)\b(?:S\d{1,2}E\d{1,2}(?:[E-]\d{1,2})*|\d{1,2}x\d{2,})\b`)

	// episodeNumberPattern extracts season/episode numbers.
	// Handles S01E01, S01E01E02 (multi-episode), S01E01-E03 (range), 5x12, Ep.5
	episodeNumberPattern = regexp.MustCompile(`(?i)\b(?:S(\d{1,2})E(\d{1,2})(?:[E-]\d{1,2})*|(\d{1,2})x(\d{2,})|Ep\.?(\d{1,2}))\b`)

	seasonNumberPattern = regexp.MustCompile(`(?i)\bS(?:eason)?[\s._-]?(\d{1,2})\b`)

	// Movie: 4-digit year (1900–2039).
	reYear = regexp.MustCompile(`\b(19\d{2}|20[0-3]\d)\b`)

	// Quality tags — all run against the normalized (dot/underscore-replaced) name.
	reResolution = regexp.MustCompile(`(?i)\b(2160p|4K(?:\s*UHD)?|1080[pi]|720p|576p|480p|360p)\b`)

	// Encoding: more-specific tokens first. Duplicate H\.264 entry removed
	// (H\.?264 already covers both H264 and H.264). AV1 before AVC to avoid
	// the AV prefix matching AVC first.
	reEncoding = regexp.MustCompile(`(?i)\b(x265|x264|HEVC|AV1|AVC|XviD|DivX|H\.?265|H\.?264)\b`)

	// Dynamic range: HDR10+ before HDR10 before HDR (prefix ordering).
	// Dolby Vision handles both dot and space separators.
	// DV with \b is safe — it only matches the standalone token.
	reDynamicRange = regexp.MustCompile(`(?i)\b(HDR10\+|HDR10|Dolby[\. ]?Vision|DV|HDR|SDR)\b`)

	// Source: more-specific variants before their shorter prefixes so the longer
	// match wins (e.g. BDRemux before BluRay, WEB-DL/WEBRip before WEB).
	reSource = regexp.MustCompile(`(?i)\b(BDRemux|BDREMUX|REMUX|BDRip|BluRay|Blu-Ray|WEB-DL|WEBDL|WEBRip|HDTV|DVDRip|DVD|PDVD|HDCAM|PDTV|WEB)\b`)

	// Release group: last hyphen-prefixed token at end of name (before any extension).
	reReleaseGroup = regexp.MustCompile(`-([A-Za-z0-9]{2,15})$`)

	// Patterns used to find where the title ends in a normalized name.
	titleCutPatterns = []*regexp.Regexp{
		episodePattern, seasonNumberPattern, seasonPackPattern,
		reYear, reResolution, reEncoding, reDynamicRange, reSource,
	}
)

func detectTV(normalized string) tvInfo {
	if m := episodeNumberPattern.FindStringSubmatch(normalized); m != nil {
		var season, episode int
		if m[1] != "" {
			season, _ = strconv.Atoi(m[1])
			episode, _ = strconv.Atoi(m[2])
		}
		if m[3] != "" {
			season, _ = strconv.Atoi(m[3])
			episode, _ = strconv.Atoi(m[4])
		}
		if m[5] != "" {
			episode, _ = strconv.Atoi(m[5])
		}
		return tvInfo{true, season, episode}
	}

	if m := seasonNumberPattern.FindStringSubmatch(normalized); m != nil {
		season, _ := strconv.Atoi(m[1])
		if season > 0 && season <= 99 && seasonPackPattern.MatchString(normalized) {
			return tvInfo{true, season, 0}
		}
	}

	if seasonPackPattern.MatchString(normalized) {
		return tvInfo{isTV: true}
	}

	return tvInfo{}
}

// normalize replaces dots and underscores with spaces and collapses whitespace.
// This converts scene-style "Show.Name.S01E01" into "Show Name S01E01".
func normalize(name string) string {
	s := strings.ReplaceAll(name, ".", " ")
	s = strings.ReplaceAll(s, "_", " ")
	return strings.Join(strings.Fields(s), " ")
}

// extractTitle returns the title portion of a normalized name by finding
// the earliest position where technical tags (year, quality, episode markers) begin.
func extractTitle(normalized string) string {
	cut := len(normalized)
	for _, p := range titleCutPatterns {
		loc := p.FindStringIndex(normalized)
		if loc == nil {
			continue
		}
		if loc[0] < cut {
			cut = loc[0]
		}
	}
	return strings.TrimSpace(normalized[:cut])
}

func firstMatch(re *regexp.Regexp, s string) string {
	return re.FindString(s)
}

// analyzeFiles returns the count of video files and the count of files whose
// extension is NOT in the allowlist.
func analyzeFiles(files []File, allowed map[string]struct{}) (videoCount, badCount int) {
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f.Path))
		if IsVideoExt(ext) {
			videoCount++
			continue
		}
		if _, ok := allowed[ext]; !ok {
			badCount++
		}
	}
	return
}

// IsVideoExt reports whether ext is a recognised video file extension.
func IsVideoExt(ext string) bool {
	switch ext {
	case ".mkv", ".mp4", ".avi", ".mov", ".wmv", ".m4v", ".ts", ".m2ts", ".vob", ".flv", ".webm":
		return true
	}
	return false
}

func shouldReject(ct gen.ContentType, files []File, totalSize int64, minSize, maxSize int64, allowed map[string]struct{}) bool {
	if totalSize < minSize || totalSize > maxSize {
		return true
	}
	if ct == gen.ContentTypeUnknown {
		return true
	}
	videoCount, badCount := analyzeFiles(files, allowed)
	if videoCount == 0 {
		return true
	}
	if badCount > 0 {
		return true
	}
	return false
}

// stripVideoExt removes a trailing video file extension from name so that
// release group detection works correctly for single-file torrents.
func stripVideoExt(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if IsVideoExt(ext) {
		return name[:len(name)-len(ext)]
	}
	return name
}

// Classify analyzes a torrent name and file list to produce a classification result.
// allowed is a pre-built set of allowed file extensions (built once at startup from config).
func Classify(name string, files []File, totalSize int64, minSize, maxSize int64, allowed map[string]struct{}) Result {
	result := Result{
		SceneName:   name,
		ContentType: gen.ContentTypeUnknown,
	}

	normalized := normalize(name)
	tv := detectTV(normalized)

	if tv.isTV {
		result.ContentType = gen.ContentTypeTv
		result.Season = tv.season
		result.Episode = tv.episode
	}

	if result.ContentType == gen.ContentTypeUnknown {
		if m := reYear.FindStringSubmatch(normalized); m != nil {
			result.ContentType = gen.ContentTypeMovie
			result.Year, _ = strconv.Atoi(m[1])
		}
	}

	if m := reYear.FindStringSubmatch(normalized); m != nil {
		result.Year, _ = strconv.Atoi(m[1])
	}

	result.Title = extractTitle(normalized)
	result.Quality = firstMatch(reResolution, normalized)
	result.Encoding = firstMatch(reEncoding, normalized)
	result.DynamicRange = firstMatch(reDynamicRange, normalized)
	result.Source = firstMatch(reSource, normalized)

	if m := reReleaseGroup.FindStringSubmatch(stripVideoExt(name)); m != nil {
		result.ReleaseGroup = m[1]
	}

	if shouldReject(result.ContentType, files, totalSize, minSize, maxSize, allowed) {
		result.State = gen.TorrentStateRejected
		return result
	}
	result.State = gen.TorrentStateClassified
	return result
}
