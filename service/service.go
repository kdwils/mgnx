package service

import (
	"fmt"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/db/gen"
)

type Service struct {
	q   gen.Querier
	cfg config.Config
}

func New(q gen.Querier, cfg config.Config) *Service {
	return &Service{q: q, cfg: cfg}
}

func magnetURL(infohash, name string) string {
	return fmt.Sprintf("magnet:?xt=urn:btih:%s&dn=%s", infohash, url.QueryEscape(name))
}

func textVal(t pgtype.Text) string {
	if t.Valid {
		return t.String
	}
	return ""
}

func int4Val(t pgtype.Int4) int32 {
	if t.Valid {
		return t.Int32
	}
	return 0
}

func timeVal(t pgtype.Timestamptz) time.Time {
	if t.Valid {
		return t.Time
	}
	return time.Time{}
}

func inferMovieCategories(quality string) []int {
	switch quality {
	case "2160p", "4K":
		return []int{CatMovies, CatMoviesUHD}
	case "1080p":
		return []int{CatMovies, CatMoviesHD}
	case "720p":
		return []int{CatMovies, CatMoviesSD}
	default:
		return []int{CatMovies}
	}
}

func inferTVCategories(quality string) []int {
	switch quality {
	case "2160p", "4K":
		return []int{CatTV, CatTVUHD}
	case "1080p":
		return []int{CatTV, CatTVHD}
	case "720p":
		return []int{CatTV, CatTVSD}
	default:
		return []int{CatTV}
	}
}

func inferCategories(contentType, quality string) []int {
	switch contentType {
	case "movie":
		return inferMovieCategories(quality)
	case "tv":
		return inferTVCategories(quality)
	default:
		return []int{CatMovies, CatTV}
	}
}
