package service

import "time"

type ListTorrentsRequest struct {
	Limit       int
	Offset      int
	State       string
	ContentType string
}

type UpdateTorrentStateRequest struct {
	Infohash string
	State    string
}

// Torznab/Newznab standard category IDs.
const (
	CatMovies        = 2000
	CatMoviesForeign = 2010
	CatMoviesOther   = 2020
	CatMoviesSD      = 2030
	CatMoviesHD      = 2040
	CatMoviesUHD     = 2045
	CatMoviesBluRay  = 2050
	CatMovies3D      = 2060
	CatTV            = 5000
	CatTVSD          = 5030
	CatTVHD          = 5040
	CatTVUHD         = 5045
	CatTVAnime       = 5070
	CatTVDocumentary = 5080
)

type SearchRequest struct {
	Query  string
	Cats   []int
	Limit  int
	Offset int
}

type MovieSearchRequest struct {
	Query  string
	ImdbID string
	Cats   []int
	Limit  int
	Offset int
}

type TVSearchRequest struct {
	Query   string
	ImdbID  string
	Season  *int32
	Episode *int32
	Cats    []int
	Limit   int
	Offset  int
}

type TorrentItem struct {
	Title        string
	Infohash     string
	MagnetURL    string
	Size         int64
	PublishedAt  time.Time
	Seeders      int64
	Leechers     int64
	Categories   []int
	Quality      string
	Encoding     string
	DynamicRange string
	Source       string
	ReleaseGroup string
	ImdbID       string
	MovieTitle   string
	Year         int32
	SeriesName   string
	Season       int32
	Episode      int32
	EpisodeName  string
}

type SearchResponse struct {
	Items []TorrentItem
}

type SearchMode struct {
	Available       bool
	SupportedParams []string
}

type Subcategory struct {
	ID   int
	Name string
}

type Category struct {
	ID            int
	Name          string
	Subcategories []Subcategory
}

type CapsResponse struct {
	MaxLimit     int
	DefaultLimit int
	SearchModes  map[string]SearchMode
	Categories   []Category
}
