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
	State           gen.TorrentState
	ContentType     gen.ContentType
	RejectionReason string
	Title           string
	Year            int
	Season          int
	Episode         int
	Quality         string
	Encoding        string
	DynamicRange    string
	Source          string
	ReleaseGroup    string
	SceneName       string
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
	// EP\d{2,4} covers absolute episode numbers used in anime scene releases (e.g. EP590).
	episodePattern = regexp.MustCompile(`(?i)\b(?:S\d{1,2}E\d{1,2}(?:[E-]\d{1,2})*|\d{1,2}x\d{2,}|EP\d{2,4})\b`)

	// episodeNumberPattern extracts season/episode numbers.
	// Handles S01E01, S01E01E02 (multi-episode), S01E01-E03 (range), 5x12, Ep.5,
	// fansub-style S2 - 01 (season-space-dash-space-episode), and EP590 absolute episodes.
	episodeNumberPattern = regexp.MustCompile(`(?i)\b(?:S(\d{1,2})E(\d{1,2})(?:[E-]\d{1,2})*|(\d{1,2})x(\d{2,})|Ep\.?(\d{1,2})|S(\d{1,2})\s+[-–]\s+(\d{1,4})|EP(\d{2,4}))\b`)

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

	// reAdult matches adult content indicators in torrent names.
	reAdult = regexp.MustCompile(`(?i)` +
		`\banal\b` + `|` +
		`\bass\b` + `|` +
		`\bblowjobs?\b` + `|` +
		`\bboob\w*` + `|` +
		`\bbitch\w*` + `|` +
		`\bcocks?\b` + `|` +
		`\bcum\w*\b` + `|` +
		`deepthroat` + `|` +
		`\bdicks?\b` + `|` +
		`\berotic\w*` + `|` +
		`fuck` + `|` +
		`gloryhole` + `|` +
		`\bhardcore\b` + `|` +
		`\bhorny\b` + `|` +
		`\bincest\b` + `|` +
		`\bkink\w*` + `|` +
		`\bmilf\w*` + `|` +
		`\bnaughty\b` + `|` +
		`\bnubile\w*` + `|` +
		`nude\w*` + `|` +
		`\bonlyfans\b` + `|` +
		`onahole` + `|` +
		`\borgasm\w*` + `|` +
		`\borgy\b` + `|` +
		`porn` + `|` +
		`\bpov\b` + `|` +
		`\bpussy\b` + `|` +
		`\bseduc\w*` + `|` +
		`\bslut\w*` + `|` +
		`step\s*bro(?:ther)?\b` + `|` +
		`step\s*mom` + `|` +
		`step\s*sis` + `|` +
		`step\s*sibling\w*` + `|` +
		`\btits?\b` + `|` +
		`\bthreesome\b` + `|` +
		`\buncensor\w*` + `|` +
		`wank` + `|` +
		`xxx` + `|` +
		// Adult studio/network names
		`\brealitykings\b` + `|` +
		`\bbrazzers` + `|` +
		`\bnaughtyamerica\b` + `|` +
		`\bbangbros\b` + `|` +
		`\bdigitalpayground\b` + `|` +
		`\bevil\s*angel\b` + `|` +
		`\bkink\.com\b` + `|` +
		`\bnubilefilms\b` + `|` +
		`\bprivate\.com\b` + `|` +
		`\bprivatemovies\b` + `|` +
		`\bwicked\s*pictures\b` + `|` +
		`woodmancasting\w*` + `|` +
		`\bvividceleb\b` + `|` +
		`\bmindgeek\b` + `|` +
		`\bmanwin\b` + `|` +
		`\bpornhub\b` + `|` +
		`\bredtube\b` + `|` +
		`\byouporn\b` + `|` +
		`\bxvideos\b` + `|` +
		`\bxhamster\b` + `|` +
		`\bxnxx\b` + `|` +
		`\bcastingcouch\w*` + `|` +
		`\bamateurallure\b` + `|` +
		`\bblacked\b` + `|` +
		`\bexploitedteens\b` + `|` +
		`\bfaceabuse\b` + `|` +
		`\bfacialabuse\b` + `|` +
		`\bfakeagent\b` + `|` +
		`\bfemalefaketaxi\b` + `|` +
		`\bmanyvids\b` + `|` +
		`\bmomlover\b` + `|` +
		`momsbangteens` + `|` +
		`\bpixy\b` + `|` +
		`\bpuremature\b` + `|` +
		`\bsinfulxxx\b` + `|` +
		`teenslikeitbig` + `|` +
		// Additional explicit terms
		`\bjav\b` + `|` +
		`\bhentai\b` + `|` +
		`\becchi\b` + `|` +
		`\beroge\b` + `|` +
		`creampie` + `|` +
		`squirt` + `|` +
		`\bnsfw\b` + `|` +
		`\bntr\b` + `|` +
		// Korean/Chinese adult markers
		`에로` + `|` +
		`三[级級]` + `|` +
		// Adult torrent site domains (appear as bracket prefixes)
		`thz\.la` + `|` +
		`7sht\.me` + `|` +
		`44x\.me` + `|` +
		`168x\.me` + `|` +
		`\b69av\b` + `|` +
		`deeper\.com` + `|` +
		`sexart\.com` + `|` +
		`topxdvd` + `|` +
		`jvid\.com` + `|` +
		`xchina` + `|` +
		`tooziq` + `|` +
		`sogclub` + `|` +
		// Additional studio/site names
		`\btokyo[-\s]?hot\b` + `|` +
		`\bchaturbate\b` + `|` +
		`\bczechcasting\b` + `|` +
		`\bczechav\b` + `|` +
		`\bheydouga\b` + `|` +
		`\bblacksonblondes\b` + `|` +
		`\bpervmom\b` + `|` +
		`\bbreedme\b` + `|` +
		`\bkellymadison\b` + `|` +
		`\bcarib\b`,
	)

	// reEncodingBroad extends reEncoding to also match space-separated variants
	// (e.g. "H 265" in names that already use spaces as separators). Used only in
	// detectAnime to strip codec tokens before checking for episode numbers, preventing
	// codec version digits (265, 264) from triggering the 3-digit episode detector.
	reEncodingBroad = regexp.MustCompile(`(?i)\b(x265|x264|HEVC|AV1|AVC|XviD|DivX|H[\. ]?265|H[\. ]?264)\b`)

	// reWWWSitePrefix strips "www.domain.tld - " style prefixes inserted by torrent
	// sites at the start of names. Matched on the normalized (dot-replaced) string.
	reWWWSitePrefix = regexp.MustCompile(`(?i)^www\s+\S+\s+\S+\s*-\s+`)

	// reBracketSitePrefix strips bracket-enclosed domain prefixes like
	// "[ FreeCourseWeb.com ] " that piracy/course sites prepend to torrent names.
	// Must be stripped before the anime bracket-prefix guard to avoid false positives.
	reBracketSitePrefix = regexp.MustCompile(`(?i)^\[[\s\w.-]{1,60}\.[a-z]{2,4}\s*\]\s*`)

	// reAnimeBracketPrefix matches any bracket tag at the very start of a name.
	// Used only for title extraction (stripping the group prefix), not for detection.
	reAnimeBracketPrefix = regexp.MustCompile(`^\[.+?\]\s*`)

	// reAnimeFansubGroup matches a known fansub/raw release group at the very start
	// of a name. The absolute-episode pattern covers lesser-known groups.
	reAnimeFansubGroup = regexp.MustCompile(`(?i)^\[(?:` +
		`SubsPlease|SubsPlus\+?|` +
		`Erai-raws|` +
		`HorribleSubs|` +
		`ASW|` +
		`Yameii|` +
		`DKB|` +
		`Judas|` +
		`Commie|` +
		`MahjongSoulless|` +
		`ANi|` +
		`SweetSub|` +
		`NanakoRaws|` +
		`AsukaRaws|` +
		`neoHEVC|` +
		`Sakurato|` +
		`Cyan|` +
		`BBF|` +
		`jibaketa|` +
		`Some-Stuffs|` +
		`Kamigami|` +
		`Studio\s+GreenTea|` +
		`TRC|` +
		`Sokudo|` +
		`Trix|` +
		`Anime\s+Land|` +
		`MagicStar|` +
		`Nekomoe\s+kissaten|` +
		`GJM|` +
		`Chihiro|` +
		`Coalgirls|` +
		`FFF|` +
		`WhyNot|` +
		`Underwater|` +
		`Ohys-Raws|` +
		`SmallSizedAnime|SSA|` +
		`DeadFish|` +
		`Eclipse|` +
		`Hiryuu|` +
		`Hatsuyuki|` +
		`Vivid` +
		`)\]`)

	// reAnimeStreamingSource matches known anime streaming services embedded in a name.
	reAnimeStreamingSource = regexp.MustCompile(`(?i)\b(?:Crunchyroll|Funimation|HiDive|HIDIVE)\b`)

	// reAnimeAbsoluteEpisode matches absolute episode numbers common in fansub releases:
	//   " 168-189"  — episode range (3+ digit to avoid cutting short title numbers)
	//   " - 01"     — SubsPlease/Erai-raws style spacer with 1–4 digit episode
	// Used for title extraction only; see reAnimeEpisodeDetect for detection.
	reAnimeAbsoluteEpisode = regexp.MustCompile(`\s+(?:\d{3,4}(?:\s*[-–]\s*\d{1,4})?|-\s+\d{1,4})\b`)

	// reAnimeEPFormat detects scene-release absolute episodes like EP590.
	// Reliable without a bracket-prefix guard since the EP token is unambiguous.
	reAnimeEPFormat = regexp.MustCompile(`\bEP\d{2,4}\b`)

	// reAnimeNumericEpisode detects 3-digit bare numbers and "- NN" spacers used in
	// fansub releases. These produce false positives for Western names with episode
	// titles that happen to contain numbers ("900 A M", "- 3 Movies", "264-PBS"),
	// so detectAnime only trusts them when the name has a bracket prefix at the start.
	reAnimeNumericEpisode = regexp.MustCompile(`\s+(?:\d{3}(?:\s*[-–]\s*\d{1,3})?|-\s+\d{1,3})\b`)

	// reAnimeBracketEpisode matches bracket-enclosed episode numbers used by some
	// fansub groups: "[01]", "[793]". Limited to 1–3 digits to avoid matching
	// quality tags like "[1080p]" or hash strings like "[B206D783]".
	// Only trusted when the name has a bracket prefix (same guard as reAnimeNumericEpisode).
	reAnimeBracketEpisode = regexp.MustCompile(`\[\d{1,3}\]`)

	// reSoftware matches software application releases. Checked against both the
	// raw name and the normalized form so dot/underscore-separated names are covered.
	reSoftware = regexp.MustCompile(`(?i)` +
		// Unambiguous software distribution tokens
		`\bRTM\b` + `|` +
		`\bLTSC\b` + `|` +
		`\bC2R\b` + `|` +
		`\bMultilingual\b` + `|` +
		`\bWinPE\b` + `|` +
		`\bx86_64\b` + `|` +
		`\bKeygen\b` + `|` +
		`\bActivator\b` + `|` +
		// Architecture tokens — distinct from codec names (x264, x265)
		`\bx64\b` + `|` +
		`\bx86\b` + `|` +
		// Operating system names with a version identifier
		`\bWindows\s+(?:XP|Vista|\d+|Server)\b` + `|` +
		// Software brands and products
		`\bAdobe\b` + `|` +
		`\bPhotoshop\b` + `|` +
		`\bIllustrator\b` + `|` +
		`\bPremiere\s+Pro\b` + `|` +
		`\bAfter\s+Effects\b` + `|` +
		`\bAutoCAD\b` + `|` +
		`\bAutodesk\b` + `|` +
		`\b3ds\s+Max\b` + `|` +
		`\bSQL\s+Server\b` + `|` +
		`\bMicrosoft\s+Office\b` + `|` +
		`\bVisual\s+Studio\b`,
	)

	// reBlockedContent matches known CSAM-related terms and distribution codes.
	// Derived from bitmagnet's banned keyword list. Checked against torrent name and all file paths.
	reBlockedContent = regexp.MustCompile(`(?i)` +
		`pa?edo(?:fil\w*|phil\w*)?` + `|` +
		`\bpreteen\b` + `|` +
		`\bpthc\b` + `|` +
		`\bptsc\b` + `|` +
		`\blsbar\b` + `|` +
		`\blsm\b` + `|` +
		`\bunderage\b` + `|` +
		`\bhebefilia\b` + `|` +
		`\bopva\b` + `|` +
		`child ?porn\w*` + `|` +
		`child ?lover\w*` + `|` +
		`porno ?child\w*` + `|` +
		`kidd(?:y\w*|ie\w*) ?porn` + `|` +
		`young ?video ?models` + `|` +
		`\bchildfugga\b` + `|` +
		`\bkinderkutje\b` + `|` +
		`\byvm\b` + `|` +
		`(?:#|1[0-7]) ?y ?o\b`,
	)

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
		if m[6] != "" {
			season, _ = strconv.Atoi(m[6])
			episode, _ = strconv.Atoi(m[7])
		}
		if m[8] != "" {
			episode, _ = strconv.Atoi(m[8])
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

// detectAnime returns true when the torrent name looks like an anime release.
// Signal A: EP-format absolute episode (EP590) — reliable without bracket context.
// Signal B: 3-digit bare numbers / "- NN" spacers / bracket episode tags — only
//
//	trusted when the name has a bracket prefix (fansub style), since these
//	patterns produce false positives for Western episode titles with numbers.
//
// Signal C: known fansub group at the start combined with any TV episode marker.
// Signal D: known anime streaming service tag combined with SxxExx TV detection.
func detectAnime(name, normalized string, tv tvInfo) bool {
	if reAdult.MatchString(name) || reAdult.MatchString(normalized) {
		return false
	}
	// Strip encoding tokens from the already-normalized (and www-stripped) string so
	// that codec version digits (265, 264) don't trigger the 3-digit episode pattern.
	normalizedNoEncoding := reEncodingBroad.ReplaceAllString(normalized, "")
	if reAnimeEPFormat.MatchString(normalizedNoEncoding) {
		return true
	}
	nameNoBracketSite := reBracketSitePrefix.ReplaceAllString(name, "")
	if reAnimeBracketPrefix.MatchString(nameNoBracketSite) &&
		(reAnimeNumericEpisode.MatchString(normalizedNoEncoding) || reAnimeBracketEpisode.MatchString(nameNoBracketSite)) {
		return true
	}
	if reAnimeFansubGroup.MatchString(name) && tv.isTV {
		return true
	}
	return tv.isTV && reAnimeStreamingSource.MatchString(normalized)
}

// extractAnimeTitle strips the bracket group prefix then applies the standard
// title-cut logic, additionally cutting at bare absolute episode numbers.
func extractAnimeTitle(normalized string) string {
	s := reAnimeBracketPrefix.ReplaceAllString(normalized, "")
	cut := len(s)
	for _, p := range titleCutPatterns {
		if loc := p.FindStringIndex(s); loc != nil && loc[0] < cut {
			cut = loc[0]
		}
	}
	if loc := reAnimeAbsoluteEpisode.FindStringIndex(s); loc != nil && loc[0] < cut {
		cut = loc[0]
	}
	return trimTitle(s[:cut])
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
	return trimTitle(normalized[:cut])
}

// trimTitle trims trailing junk left when a title-cut pattern lands just after a
// separator or an opening bracket/paren (e.g. " - S04E01", "Title (1991)").
func trimTitle(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), " -–_.([")
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
	case ".mkv", ".mp4", ".avi", ".mov", ".wmv", ".m4v", ".ts", ".m2ts", ".vob", ".flv", ".webm",
		".iso", ".mpg", ".mpeg":
		return true
	}
	return false
}

// ContainsBlockedContent reports whether the torrent name or any file path
// matches the blocked content pattern. Used to drop torrents before any DB writes.
func ContainsBlockedContent(name string, files []File) bool {
	if reBlockedContent.MatchString(normalize(name)) {
		return true
	}
	for _, f := range files {
		if reBlockedContent.MatchString(normalize(f.Path)) {
			return true
		}
	}
	return false
}

func shouldReject(ct gen.ContentType, name, normalized string, files []File, totalSize int64, minSize, maxSize int64, allowed map[string]struct{}, enableExtensionFilter, excludeAdultContent bool) string {
	if totalSize < minSize || totalSize > maxSize {
		return "size"
	}
	if excludeAdultContent && (reAdult.MatchString(name) || reAdult.MatchString(normalized)) {
		return "adult"
	}
	if reSoftware.MatchString(name) || reSoftware.MatchString(normalized) {
		return "software"
	}
	if ct == gen.ContentTypeUnknown {
		return "unknown_type"
	}
	if len(files) > 0 {
		videoCount, badCount := analyzeFiles(files, allowed)
		if videoCount == 0 {
			return "no_video"
		}
		if enableExtensionFilter && badCount > 0 {
			return "bad_extension"
		}
	}
	return ""
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
// enableExtensionFilter controls whether to reject torrents with non-allowed file extensions.
// excludeAdultContent controls whether to reject torrents whose name contains XXX.
func Classify(name string, files []File, totalSize int64, minSize, maxSize int64, allowed map[string]struct{}, enableExtensionFilter, excludeAdultContent bool) Result {
	result := Result{
		SceneName:   name,
		ContentType: gen.ContentTypeUnknown,
	}

	normalized := reWWWSitePrefix.ReplaceAllString(normalize(name), "")
	tv := detectTV(normalized)

	switch {
	case detectAnime(name, normalized, tv):
		result.ContentType = gen.ContentTypeAnime
		result.Season = tv.season
		result.Episode = tv.episode
		result.Title = extractAnimeTitle(normalized)
	case tv.isTV:
		result.ContentType = gen.ContentTypeTv
		result.Season = tv.season
		result.Episode = tv.episode
		result.Title = extractTitle(normalized)
	default:
		if reYear.MatchString(normalized) || reResolution.MatchString(normalized) {
			result.ContentType = gen.ContentTypeMovie
		}
		result.Title = extractTitle(normalized)
	}

	if m := reYear.FindStringSubmatch(normalized); m != nil {
		result.Year, _ = strconv.Atoi(m[1])
	}
	result.Quality = firstMatch(reResolution, normalized)
	result.Encoding = firstMatch(reEncoding, normalized)
	result.DynamicRange = firstMatch(reDynamicRange, normalized)
	result.Source = firstMatch(reSource, normalized)

	if m := reReleaseGroup.FindStringSubmatch(stripVideoExt(name)); m != nil {
		result.ReleaseGroup = m[1]
	}

	if reason := shouldReject(result.ContentType, name, normalized, files, totalSize, minSize, maxSize, allowed, enableExtensionFilter, excludeAdultContent); reason != "" {
		result.State = gen.TorrentStateRejected
		result.RejectionReason = reason
		return result
	}
	result.State = gen.TorrentStateClassified
	return result
}
