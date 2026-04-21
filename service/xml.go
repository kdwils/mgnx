package service

import (
	"encoding/xml"
	"strconv"
	"strings"
	"time"
)

type XMLCaps struct {
	XMLName    xml.Name          `xml:"caps"`
	Server     XMLCapsServer     `xml:"server"`
	Limits     XMLCapsLimits     `xml:"limits"`
	Searching  XMLSearching      `xml:"searching"`
	Categories XMLCapsCategories `xml:"categories"`
}

type XMLCapsServer struct {
	Title string `xml:"title,attr"`
}

type XMLCapsLimits struct {
	Max     int `xml:"max,attr"`
	Default int `xml:"default,attr"`
}

type XMLSearching struct {
	Search      XMLSearchMode `xml:"search"`
	MovieSearch XMLSearchMode `xml:"movie-search"`
	TVSearch    XMLSearchMode `xml:"tv-search"`
	AudioSearch XMLSearchMode `xml:"audio-search"`
	BookSearch  XMLSearchMode `xml:"book-search"`
}

type XMLSearchMode struct {
	Available       string `xml:"available,attr"`
	SupportedParams string `xml:"supportedParams,attr"`
}

type XMLCapsCategories struct {
	Categories []XMLCategory `xml:"category"`
}

type XMLCategory struct {
	ID            int              `xml:"id,attr"`
	Name          string           `xml:"name,attr"`
	Subcategories []XMLSubcategory `xml:"subcat"`
}

type XMLSubcategory struct {
	ID   int    `xml:"id,attr"`
	Name string `xml:"name,attr"`
}

func newXMLCaps(caps CapsResponse) XMLCaps {
	searching := XMLSearching{
		AudioSearch: XMLSearchMode{Available: "no", SupportedParams: ""},
		BookSearch:  XMLSearchMode{Available: "no", SupportedParams: ""},
	}
	for mode, sm := range caps.SearchModes {
		m := XMLSearchMode{
			Available:       "no",
			SupportedParams: strings.Join(sm.SupportedParams, ","),
		}
		if sm.Available {
			m.Available = "yes"
		}
		switch mode {
		case "search":
			searching.Search = m
		case "movie-search":
			searching.MovieSearch = m
		case "tv-search":
			searching.TVSearch = m
		}
	}

	categories := make([]XMLCategory, 0, len(caps.Categories))
	for _, c := range caps.Categories {
		subs := make([]XMLSubcategory, 0, len(c.Subcategories))
		for _, sc := range c.Subcategories {
			subs = append(subs, XMLSubcategory{ID: sc.ID, Name: sc.Name})
		}
		categories = append(categories, XMLCategory{ID: c.ID, Name: c.Name, Subcategories: subs})
	}

	return XMLCaps{
		Server:     XMLCapsServer{Title: "mgnx"},
		Limits:     XMLCapsLimits{Max: caps.MaxLimit, Default: caps.DefaultLimit},
		Searching:  searching,
		Categories: XMLCapsCategories{Categories: categories},
	}
}

type XMLRSS struct {
	XMLName   xml.Name   `xml:"rss"`
	Version   string     `xml:"version,attr,omitempty"`
	TorznabNS string     `xml:"xmlns:torznab,attr"`
	Channel   XMLChannel `xml:"channel"`
}

type XMLChannel struct {
	Title       string    `xml:"title"`
	Description string    `xml:"description"`
	Items       []XMLItem `xml:"item"`
}

type XMLItem struct {
	XMLName   xml.Name     `xml:"item"`
	Title     string       `xml:"title"`
	GUID      XMLGUID      `xml:"guid"`
	Link      string       `xml:"link"`
	Enclosure XMLEnclosure `xml:"enclosure"`
	Size      int64        `xml:"size"`
	PubDate   string       `xml:"pubDate"`
	Attrs     []XMLTorznabAttr
}

type XMLEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type XMLGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

// XMLTorznabAttr encodes as <torznab:attr name="..." value="..."/>.
type XMLTorznabAttr struct {
	XMLName xml.Name `xml:"torznab:attr"`
	Name    string   `xml:"name,attr"`
	Value   string   `xml:"value,attr"`
}

func newXMLRSS(items []TorrentItem) XMLRSS {
	xmlItems := make([]XMLItem, 0, len(items))
	for _, item := range items {
		xmlItems = append(xmlItems, newXMLItem(item))
	}
	return XMLRSS{
		Version:   "2.0",
		TorznabNS: "http://torznab.com/schemas/2015/feed",
		Channel: XMLChannel{
			Title:       "mgnx",
			Description: "mgnx Torznab indexer",
			Items:       xmlItems,
		},
	}
}

func newXMLItem(t TorrentItem) XMLItem {
	attrs := []XMLTorznabAttr{
		torznabAttr("seeders", strconv.FormatInt(t.Seeders, 10)),
		torznabAttr("leechers", strconv.FormatInt(t.Leechers, 10)),
		torznabAttr("infohash", t.Infohash),
	}
	for _, cat := range t.Categories {
		attrs = append(attrs, torznabAttr("category", strconv.Itoa(cat)))
	}
	if t.ImdbID != "" {
		attrs = append(attrs, torznabAttr("imdbid", t.ImdbID))
	}
	if t.Quality != "" {
		attrs = append(attrs, torznabAttr("resolution", t.Quality))
	}
	if t.Encoding != "" {
		attrs = append(attrs, torznabAttr("encoding", t.Encoding))
	}
	if t.DynamicRange != "" {
		attrs = append(attrs, torznabAttr("dynamicrange", t.DynamicRange))
	}
	if t.Source != "" {
		attrs = append(attrs, torznabAttr("source", t.Source))
	}
	if t.ReleaseGroup != "" {
		attrs = append(attrs, torznabAttr("releasegroup", t.ReleaseGroup))
	}
	if t.SeriesName != "" {
		attrs = append(attrs, torznabAttr("series", t.SeriesName))
	}
	if t.Season > 0 {
		attrs = append(attrs, torznabAttr("season", strconv.Itoa(int(t.Season))))
	}
	if t.Episode > 0 {
		attrs = append(attrs, torznabAttr("episode", strconv.Itoa(int(t.Episode))))
	}
	if t.MovieTitle != "" {
		attrs = append(attrs, torznabAttr("title", t.MovieTitle))
	}
	if t.Year > 0 {
		attrs = append(attrs, torznabAttr("year", strconv.Itoa(int(t.Year))))
	}

	pubDate := ""
	if !t.PublishedAt.IsZero() {
		pubDate = t.PublishedAt.UTC().Format(time.RFC1123Z)
	}

	return XMLItem{
		Title: t.Title,
		GUID:  XMLGUID{IsPermaLink: "false", Value: t.Infohash},
		Link:  t.MagnetURL,
		Enclosure: XMLEnclosure{
			URL:    t.MagnetURL,
			Length: t.Size,
			Type:   "application/x-bittorrent;x-scheme-handler/magnet",
		},
		Size:    t.Size,
		PubDate: pubDate,
		Attrs:   attrs,
	}
}

func torznabAttr(name, value string) XMLTorznabAttr {
	return XMLTorznabAttr{Name: name, Value: value}
}

type XMLError struct {
	XMLName     xml.Name `xml:"error"`
	Code        int      `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}
