package client

import (
	"net/url"
	"time"
)

// Torrent is the unified representation returned by both List and Get.
// Field names mirror the JSON keys from GET /api/torrents and GET /api/torrents/{infohash}.
type Torrent struct {
	Infohash     string    `json:"infohash"`
	Name         string    `json:"name"`
	TotalSize    int64     `json:"total_size"`
	FileCount    int64     `json:"file_count"`
	State        string    `json:"state"`
	ContentType  string    `json:"content_type"`
	Quality      string    `json:"quality"`
	Encoding     string    `json:"encoding"`
	DynamicRange string    `json:"dynamic_range"`
	Source       string    `json:"source"`
	ReleaseGroup string    `json:"release_group"`
	Seeders      int64     `json:"seeders"`
	Leechers     int64     `json:"leechers"`
	FirstSeen    time.Time `json:"first_seen"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (t Torrent) MagnetURL() string {
	return "magnet:?xt=urn:btih:" + t.Infohash + "&dn=" + url.QueryEscape(t.Name)
}

type ListParams struct {
	Limit       int
	Offset      int
	State       string
	ContentType string
}

type HealthCheck struct {
	Ok      bool     `json:"ok"`
	Reasons []string `json:"reasons,omitempty"`
}

// ValidStates is the exhaustive set accepted by PATCH /api/torrents/{infohash}.
var ValidStates = []string{"pending", "classified", "enriched", "active", "dead", "rejected"}
