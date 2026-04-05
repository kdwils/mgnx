package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kdwils/mgnx/db/gen"
)

func (s *Service) Search(ctx context.Context, req SearchRequest) (XMLRSS, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}

	rows, err := s.q.SearchAll(ctx, gen.SearchAllParams{
		Query:      pgtype.Text{String: req.Query, Valid: req.Query != ""},
		PageOffset: int32(req.Offset),
		PageSize:   int32(limit),
	})
	if err != nil {
		return XMLRSS{}, err
	}

	items := make([]TorrentItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, TorrentItem{
			Title:        r.Name,
			Infohash:     r.Infohash,
			MagnetURL:    magnetURL(r.Infohash, r.Name),
			Size:         r.TotalSize,
			PublishedAt:  timeVal(r.FirstSeen),
			Seeders:      r.Seeders,
			Leechers:     r.Leechers,
			Categories:   inferCategories(string(r.ContentType), textVal(r.Quality)),
			Quality:      textVal(r.Quality),
			Encoding:     textVal(r.Encoding),
			DynamicRange: textVal(r.DynamicRange),
			Source:       textVal(r.Source),
			ReleaseGroup: textVal(r.ReleaseGroup),
		})
	}

	return newXMLRSS(items), nil
}

func (s *Service) SearchMovies(ctx context.Context, req MovieSearchRequest) (XMLRSS, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}

	if req.ImdbID != "" {
		rows, err := s.q.GetMoviesByIMDB(ctx, pgtype.Text{String: req.ImdbID, Valid: true})
		if err != nil {
			return XMLRSS{}, err
		}
		items := make([]TorrentItem, 0, len(rows))
		for _, r := range rows {
			items = append(items, TorrentItem{
				Title:        r.Name,
				Infohash:     r.Infohash,
				MagnetURL:    magnetURL(r.Infohash, r.Name),
				Size:         r.TotalSize,
				PublishedAt:  timeVal(r.FirstSeen),
				Seeders:      r.Seeders,
				Leechers:     r.Leechers,
				Categories:   inferMovieCategories(textVal(r.Quality)),
				Quality:      textVal(r.Quality),
				Encoding:     textVal(r.Encoding),
				DynamicRange: textVal(r.DynamicRange),
				Source:       textVal(r.Source),
				ReleaseGroup: textVal(r.ReleaseGroup),
				ImdbID:       textVal(r.ImdbID),
				MovieTitle:   r.MovieTitle,
				Year:         int4Val(r.Year),
			})
		}
		return newXMLRSS(items), nil
	}

	rows, err := s.q.SearchMovies(ctx, gen.SearchMoviesParams{
		Query:      pgtype.Text{String: req.Query, Valid: req.Query != ""},
		PageOffset: int32(req.Offset),
		PageSize:   int32(limit),
	})
	if err != nil {
		return XMLRSS{}, err
	}

	items := make([]TorrentItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, TorrentItem{
			Title:        r.Name,
			Infohash:     r.Infohash,
			MagnetURL:    magnetURL(r.Infohash, r.Name),
			Size:         r.TotalSize,
			PublishedAt:  timeVal(r.FirstSeen),
			Seeders:      r.Seeders,
			Leechers:     r.Leechers,
			Categories:   inferMovieCategories(textVal(r.Quality)),
			Quality:      textVal(r.Quality),
			Encoding:     textVal(r.Encoding),
			DynamicRange: textVal(r.DynamicRange),
			Source:       textVal(r.Source),
			ReleaseGroup: textVal(r.ReleaseGroup),
			ImdbID:       textVal(r.ImdbID),
			MovieTitle:   r.MovieTitle,
			Year:         int4Val(r.Year),
		})
	}
	return newXMLRSS(items), nil
}

func (s *Service) SearchTV(ctx context.Context, req TVSearchRequest) (XMLRSS, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}

	if req.ImdbID != "" {
		params := gen.GetTVByIMDBParams{
			ImdbID: pgtype.Text{String: req.ImdbID, Valid: true},
		}
		if req.Season != nil {
			params.Season = pgtype.Int4{Int32: *req.Season, Valid: true}
		}
		if req.Episode != nil {
			params.Episode = pgtype.Int4{Int32: *req.Episode, Valid: true}
		}

		rows, err := s.q.GetTVByIMDB(ctx, params)
		if err != nil {
			return XMLRSS{}, err
		}
		items := make([]TorrentItem, 0, len(rows))
		for _, r := range rows {
			items = append(items, TorrentItem{
				Title:        r.Name,
				Infohash:     r.Infohash,
				MagnetURL:    magnetURL(r.Infohash, r.Name),
				Size:         r.TotalSize,
				PublishedAt:  timeVal(r.FirstSeen),
				Seeders:      r.Seeders,
				Leechers:     r.Leechers,
				Categories:   inferTVCategories(textVal(r.Quality)),
				Quality:      textVal(r.Quality),
				Encoding:     textVal(r.Encoding),
				DynamicRange: textVal(r.DynamicRange),
				Source:       textVal(r.Source),
				ReleaseGroup: textVal(r.ReleaseGroup),
				ImdbID:       textVal(r.ImdbID),
				SeriesName:   r.SeriesName,
				Season:       int4Val(r.Season),
				Episode:      int4Val(r.Episode),
				EpisodeName:  textVal(r.EpisodeName),
			})
		}
		return newXMLRSS(items), nil
	}

	params := gen.SearchTVParams{
		Query:      pgtype.Text{String: req.Query, Valid: req.Query != ""},
		PageOffset: int32(req.Offset),
		PageSize:   int32(limit),
	}
	if req.Season != nil {
		params.Season = pgtype.Int4{Int32: *req.Season, Valid: true}
	}
	if req.Episode != nil {
		params.Episode = pgtype.Int4{Int32: *req.Episode, Valid: true}
	}

	rows, err := s.q.SearchTV(ctx, params)
	if err != nil {
		return XMLRSS{}, err
	}

	items := make([]TorrentItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, TorrentItem{
			Title:        r.Name,
			Infohash:     r.Infohash,
			MagnetURL:    magnetURL(r.Infohash, r.Name),
			Size:         r.TotalSize,
			PublishedAt:  timeVal(r.FirstSeen),
			Seeders:      r.Seeders,
			Leechers:     r.Leechers,
			Categories:   inferTVCategories(textVal(r.Quality)),
			Quality:      textVal(r.Quality),
			Encoding:     textVal(r.Encoding),
			DynamicRange: textVal(r.DynamicRange),
			Source:       textVal(r.Source),
			ReleaseGroup: textVal(r.ReleaseGroup),
			ImdbID:       textVal(r.ImdbID),
			SeriesName:   r.SeriesName,
			Season:       int4Val(r.Season),
			Episode:      int4Val(r.Episode),
			EpisodeName:  textVal(r.EpisodeName),
		})
	}
	return newXMLRSS(items), nil
}
