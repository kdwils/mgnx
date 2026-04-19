package indexer

import (
	"context"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kdwils/mgnx/classify"
	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/db/gen"
)

// IndexResult summarises a completed index run.
type IndexResult struct {
	Scanned int
	Updated int
}

// Indexer reclassifies all torrents already in the database.
type Indexer struct {
	queries gen.Querier
	cfg     config.Indexer
	allowed map[string]struct{}
}

func allowedExtensions(exts []string) map[string]struct{} {
	m := make(map[string]struct{}, len(exts))
	for _, ext := range exts {
		m[ext] = struct{}{}
	}
	return m
}

// NewIndexer creates an Indexer.
func NewIndexer(queries gen.Querier, cfg config.Indexer) *Indexer {
	return &Indexer{queries: queries, cfg: cfg, allowed: allowedExtensions(cfg.AllowedExtensions)}
}

// Run iterates over all torrents and reclassifies each one.
// When dryRun is true changes are written to out instead of the database.
func (ix *Indexer) Run(ctx context.Context, dryRun bool, out io.Writer) (IndexResult, error) {
	const batchSize = int32(500)

	total, err := ix.queries.CountTorrents(ctx)
	if err != nil {
		return IndexResult{}, fmt.Errorf("counting torrents: %w", err)
	}

	var res IndexResult

	for offset := int64(0); offset < total; offset += int64(batchSize) {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		batch, err := ix.queries.GetTorrentsToIndex(ctx, gen.GetTorrentsToIndexParams{
			Limit:  batchSize,
			Offset: int32(offset),
		})
		if err != nil {
			return res, fmt.Errorf("fetching torrents: %w", err)
		}

		for _, t := range batch {
			res.Scanned++

			files, err := ix.queries.GetTorrentFiles(ctx, t.Infohash)
			if err != nil {
				return res, fmt.Errorf("fetching files for %s: %w", t.Infohash, err)
			}

			classifyFiles := make([]classify.File, len(files))
			for i, f := range files {
				classifyFiles[i] = classify.File{Path: f.Path, Size: f.Size}
			}

			result := classify.Classify(
				t.Name,
				classifyFiles,
				t.TotalSize,
				ix.cfg.MinSize,
				ix.cfg.MaxSize,
				ix.allowed,
				ix.cfg.EnableExtensionFilter,
				ix.cfg.ExcludeAdultContent,
			)

			newState := resolveState(t.State, result.State)

			if result.ContentType == t.ContentType && newState == t.State {
				continue
			}

			res.Updated++

			if dryRun {
				stateChange := ""
				if newState != t.State {
					stateChange = fmt.Sprintf(", state %s→%s", t.State, newState)
				}
				fmt.Fprintf(out, "%s %q: content_type %s→%s%s\n",
					t.Infohash, t.Name, t.ContentType, result.ContentType, stateChange)
				continue
			}

			err = ix.queries.UpdateTorrentClassified(ctx, classifyParams(t.Infohash, newState, result))
			if err != nil {
				return res, fmt.Errorf("updating %s: %w", t.Infohash, err)
			}
		}
	}

	return res, nil
}

// resolveState determines the new state after classification.
// pending and rejected torrents may transition based on the classify result;
// all other states are preserved so active/dead/enriched torrents are not demoted.
func resolveState(current, classified gen.TorrentState) gen.TorrentState {
	switch current {
	case gen.TorrentStatePending, gen.TorrentStateRejected:
		return classified
	default:
		return current
	}
}

func classifyParams(infohash string, state gen.TorrentState, r classify.Result) gen.UpdateTorrentClassifiedParams {
	return gen.UpdateTorrentClassifiedParams{
		Infohash:          infohash,
		State:             state,
		ContentType:       r.ContentType,
		Quality:           nullText(r.Quality),
		Encoding:          nullText(r.Encoding),
		DynamicRange:      nullText(r.DynamicRange),
		Source:            nullText(r.Source),
		ReleaseGroup:      nullText(r.ReleaseGroup),
		SceneName:         nullText(r.SceneName),
		ClassifiedTitle:   nullText(r.Title),
		ClassifiedYear:    nullInt4(r.Year),
		ClassifiedSeason:  nullInt4(r.Season),
		ClassifiedEpisode: nullInt4(r.Episode),
	}
}

func nullText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func nullInt4(n int) pgtype.Int4 {
	if n == 0 {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(n), Valid: true}
}
