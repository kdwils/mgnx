package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/kdwils/mgnx/db/gen"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrInvalidState = errors.New("invalid state")
)

func (s *Service) ListTorrents(ctx context.Context, req ListTorrentsRequest) ([]gen.ListTorrentsRow, error) {
	params := gen.ListTorrentsParams{
		PageSize:   int32(req.Limit),
		PageOffset: int32(req.Offset),
	}
	if req.State != "" {
		params.State = gen.NullTorrentState{TorrentState: gen.TorrentState(req.State), Valid: true}
	}
	if req.ContentType != "" {
		params.ContentType = gen.NullContentType{ContentType: gen.ContentType(req.ContentType), Valid: true}
	}

	rows, err := s.q.ListTorrents(ctx, params)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []gen.ListTorrentsRow{}, nil
	}
	return rows, nil
}

func (s *Service) GetTorrent(ctx context.Context, infohash string) (gen.GetTorrentByInfohashRow, error) {
	row, err := s.q.GetTorrentByInfohash(ctx, infohash)
	if errors.Is(err, pgx.ErrNoRows) {
		return gen.GetTorrentByInfohashRow{}, ErrNotFound
	}
	return row, err
}

func (s *Service) UpdateTorrentState(ctx context.Context, req UpdateTorrentStateRequest) error {
	state := gen.TorrentState(req.State)
	switch state {
	case gen.TorrentStatePending, gen.TorrentStateClassified, gen.TorrentStateEnriched,
		gen.TorrentStateActive, gen.TorrentStateDead, gen.TorrentStateRejected:
	default:
		return fmt.Errorf("%w: %s", ErrInvalidState, req.State)
	}
	return s.q.UpdateTorrentState(ctx, gen.UpdateTorrentStateParams{
		Infohash: req.Infohash,
		State:    state,
	})
}

func (s *Service) DeleteTorrent(ctx context.Context, infohash string) error {
	return s.q.DeleteTorrent(ctx, infohash)
}
