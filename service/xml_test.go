package service

import (
	"encoding/xml"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewXMLItem(t *testing.T) {
	published := time.Date(2026, time.April, 9, 16, 57, 57, 0, time.UTC)
	item := TorrentItem{
		Title:        "Test.Movie.2024.1080p.BluRay.x264-GROUP",
		Infohash:     "bddb5ff04822e617d62c503e9087a4ec2b318862",
		MagnetURL:    "magnet:?xt=urn:btih:bddb5ff04822e617d62c503e9087a4ec2b318862&dn=Test.Movie",
		Size:         19978580639,
		PublishedAt:  published,
		Seeders:      2,
		Leechers:     0,
		Categories:   []int{2000, 2040},
		Quality:      "1080p",
		Encoding:     "x264",
		Source:       "BluRay",
		ReleaseGroup: "GROUP",
	}

	result := newXMLItem(item)
	xmlBytes, err := xml.Marshal(result)
	require.NoError(t, err)
	xmlStr := string(xmlBytes)

	want := `<item><title>Test.Movie.2024.1080p.BluRay.x264-GROUP</title><guid isPermaLink="false">bddb5ff04822e617d62c503e9087a4ec2b318862</guid><link>magnet:?xt=urn:btih:bddb5ff04822e617d62c503e9087a4ec2b318862&amp;dn=Test.Movie</link><size>19978580639</size><pubDate>Thu, 09 Apr 2026 16:57:57 +0000</pubDate><torznab:attr name="seeders" value="2"></torznab:attr><torznab:attr name="leechers" value="0"></torznab:attr><torznab:attr name="infohash" value="bddb5ff04822e617d62c503e9087a4ec2b318862"></torznab:attr><torznab:attr name="category" value="2000"></torznab:attr><torznab:attr name="category" value="2040"></torznab:attr><torznab:attr name="resolution" value="1080p"></torznab:attr><torznab:attr name="encoding" value="x264"></torznab:attr><torznab:attr name="source" value="BluRay"></torznab:attr><torznab:attr name="releasegroup" value="GROUP"></torznab:attr></item>`

	assert.Equal(t, want, xmlStr)
}

func TestNewXMLItem_MovieEnrichment(t *testing.T) {
	item := TorrentItem{
		Title:        "The Matrix 1999 1080p BluRay",
		Infohash:     "abc123",
		MagnetURL:    "magnet:?xt=urn:btih:abc123",
		Size:         8 * 1024 * 1024 * 1024,
		PublishedAt:  time.Date(2026, time.April, 9, 10, 0, 0, 0, time.UTC),
		Seeders:      50,
		Leechers:     5,
		Categories:   []int{2000},
		Quality:      "1080p",
		Encoding:     "x264",
		Source:       "BluRay",
		ReleaseGroup: "FGT",
		ImdbID:       "tt0133093",
		MovieTitle:   "The Matrix",
		Year:         1999,
	}

	result := newXMLItem(item)
	xmlBytes, err := xml.Marshal(result)
	require.NoError(t, err)
	xmlStr := string(xmlBytes)

	want := `<item><title>The Matrix 1999 1080p BluRay</title><guid isPermaLink="false">abc123</guid><link>magnet:?xt=urn:btih:abc123</link><size>8589934592</size><pubDate>Thu, 09 Apr 2026 10:00:00 +0000</pubDate><torznab:attr name="seeders" value="50"></torznab:attr><torznab:attr name="leechers" value="5"></torznab:attr><torznab:attr name="infohash" value="abc123"></torznab:attr><torznab:attr name="category" value="2000"></torznab:attr><torznab:attr name="imdbid" value="tt0133093"></torznab:attr><torznab:attr name="resolution" value="1080p"></torznab:attr><torznab:attr name="encoding" value="x264"></torznab:attr><torznab:attr name="source" value="BluRay"></torznab:attr><torznab:attr name="releasegroup" value="FGT"></torznab:attr><torznab:attr name="title" value="The Matrix"></torznab:attr><torznab:attr name="year" value="1999"></torznab:attr></item>`

	assert.Equal(t, want, xmlStr)
}

func TestNewXMLItem_TVEnrichment(t *testing.T) {
	item := TorrentItem{
		Title:        "Breaking Bad S01E01 720p BluRay",
		Infohash:     "def456",
		MagnetURL:    "magnet:?xt=urn:btih:def456",
		Size:         4 * 1024 * 1024 * 1024,
		PublishedAt:  time.Date(2026, time.April, 9, 10, 0, 0, 0, time.UTC),
		Seeders:      25,
		Leechers:     2,
		Categories:   []int{5000, 5040},
		Quality:      "720p",
		Encoding:     "x264",
		Source:       "BluRay",
		ReleaseGroup: "GROUP",
		SeriesName:   "Breaking Bad",
		Season:       1,
		Episode:      1,
		EpisodeName:  "Pilot",
	}

	result := newXMLItem(item)
	xmlBytes, err := xml.Marshal(result)
	require.NoError(t, err)
	xmlStr := string(xmlBytes)

	want := `<item><title>Breaking Bad S01E01 720p BluRay</title><guid isPermaLink="false">def456</guid><link>magnet:?xt=urn:btih:def456</link><size>4294967296</size><pubDate>Thu, 09 Apr 2026 10:00:00 +0000</pubDate><torznab:attr name="seeders" value="25"></torznab:attr><torznab:attr name="leechers" value="2"></torznab:attr><torznab:attr name="infohash" value="def456"></torznab:attr><torznab:attr name="category" value="5000"></torznab:attr><torznab:attr name="category" value="5040"></torznab:attr><torznab:attr name="resolution" value="720p"></torznab:attr><torznab:attr name="encoding" value="x264"></torznab:attr><torznab:attr name="source" value="BluRay"></torznab:attr><torznab:attr name="releasegroup" value="GROUP"></torznab:attr><torznab:attr name="series" value="Breaking Bad"></torznab:attr><torznab:attr name="season" value="1"></torznab:attr><torznab:attr name="episode" value="1"></torznab:attr></item>`

	assert.Equal(t, want, xmlStr)
}

func TestXMLRSS_Namespace(t *testing.T) {
	items := []TorrentItem{
		{
			Title:    "Test",
			Infohash: "abc123",
			Size:     1024,
			Seeders:  1,
		},
	}

	rss := newXMLRSS(items)
	xmlBytes, err := xml.Marshal(rss)
	require.NoError(t, err)
	xmlStr := string(xmlBytes)

	want := `<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><title>mgnx</title><description>mgnx Torznab indexer</description><item><title>Test</title><guid isPermaLink="false">abc123</guid><link></link><size>1024</size><pubDate></pubDate><torznab:attr name="seeders" value="1"></torznab:attr><torznab:attr name="leechers" value="0"></torznab:attr><torznab:attr name="infohash" value="abc123"></torznab:attr></item></channel></rss>`

	assert.Equal(t, want, xmlStr)
}
