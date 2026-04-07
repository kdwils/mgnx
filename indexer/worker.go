package indexer

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kdwils/mgnx/classify"
	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/db/gen"
	"github.com/kdwils/mgnx/dht"
	"github.com/kdwils/mgnx/logger"
	"github.com/kdwils/mgnx/metadata"
	"golang.org/x/time/rate"
)

type Worker struct {
	crawler     dht.Crawler
	fetcher     metadata.Fetcher
	queries     gen.Querier
	cfg         config.Indexer
	allowedExts map[string]struct{}
	peerTimeout time.Duration
	maxPeers    int
	rateLimiter *rate.Limiter
	minSize     int64
	maxSize     int64
}

func New(crawler dht.Crawler, fetcher metadata.Fetcher, queries gen.Querier, cfg config.Indexer) *Worker {
	allowed := make(map[string]struct{}, len(cfg.AllowedExtensions))
	for _, ext := range cfg.AllowedExtensions {
		allowed[ext] = struct{}{}
	}
	return &Worker{
		crawler:     crawler,
		fetcher:     fetcher,
		queries:     queries,
		cfg:         cfg,
		allowedExts: allowed,
		peerTimeout: cfg.RequestTimeout,
		maxPeers:    cfg.MaxPeers,
		rateLimiter: rate.NewLimiter(rate.Limit(cfg.RateLimit), cfg.RateBurst),
		minSize:     cfg.MinSize,
		maxSize:     cfg.MaxSize,
	}
}

func (w *Worker) Run(ctx context.Context) {
	workers := w.cfg.Workers
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for {
		select {
		case ev, ok := <-w.crawler.Infohashes():
			if !ok {
				wg.Wait()
				return
			}
			sem <- struct{}{}
			wg.Add(1)
			go func(e dht.DiscoveredPeers) {
				defer wg.Done()
				defer func() { <-sem }()
				w.process(ctx, e)
			}(ev)
		case <-ctx.Done():
			wg.Wait()
			return
		}
	}
}

func (w *Worker) process(ctx context.Context, ev dht.DiscoveredPeers) {
	log := logger.FromContext(ctx)
	infohashHex := hex.EncodeToString(ev.Infohash[:])

	_, err := w.queries.GetTorrentByInfohash(ctx, infohashHex)
	if err == nil {
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		log.ErrorContext(ctx, "db lookup failed", "infohash", infohashHex, "err", err)
		return
	}

	retries := w.maxPeers
	if len(ev.Peers) < retries {
		retries = len(ev.Peers)
	}

	var info *metadata.TorrentInfo
	for i := 0; i < retries; i++ {
		// Rate limit between peer attempts (after first peer)
		if i > 0 {
			if err := w.rateLimiter.Wait(ctx); err != nil {
				break
			}
		}

		peer := ev.Peers[i]
		addr := net.TCPAddr{IP: peer.SourceIP, Port: peer.Port}

		fetchCtx, cancel := context.WithTimeout(ctx, w.peerTimeout)
		info, err = w.fetcher.Fetch(fetchCtx, ev.Infohash, addr)
		cancel()
		if err == nil {
			break
		}
		log.DebugContext(ctx, "metadata fetch failed", "infohash", infohashHex, "addr", addr.String(), "err", err)
	}

	if info == nil {
		return
	}

	tag, err := w.queries.UpsertTorrentPending(ctx, gen.UpsertTorrentPendingParams{
		Infohash:  infohashHex,
		Name:      info.Name,
		TotalSize: info.TotalSize,
		FileCount: int64(len(info.Files)),
	})
	if err != nil {
		log.ErrorContext(ctx, "upsert torrent failed", "infohash", infohashHex, "err", err)
		return
	}

	if tag.RowsAffected() == 0 {
		return
	}

	classifyFiles := make([]classify.File, len(info.Files))
	for i, f := range info.Files {
		ext := filepath.Ext(f.Path)
		var extension pgtype.Text
		if ext != "" {
			extension = pgtype.Text{String: ext, Valid: true}
		}

		if err := w.queries.InsertTorrentFile(ctx, gen.InsertTorrentFileParams{
			Infohash:  infohashHex,
			Path:      sanitizePath(f.Path),
			Size:      f.Size,
			Extension: extension,
			IsVideo:   classify.IsVideoExt(ext),
		}); err != nil {
			log.ErrorContext(ctx, "insert torrent file failed", "infohash", infohashHex, "path", f.Path, "err", err)
		}

		classifyFiles[i] = classify.File{Path: f.Path, Size: f.Size}
	}

	result := classify.Classify(info.Name, classifyFiles, info.TotalSize, w.minSize, w.maxSize, w.allowedExts, w.cfg.EnableExtensionFilter)
	if err := w.queries.UpdateTorrentClassified(ctx, gen.UpdateTorrentClassifiedParams{
		Infohash:          infohashHex,
		State:             result.State,
		ContentType:       result.ContentType,
		Quality:           nullText(result.Quality),
		Encoding:          nullText(result.Encoding),
		DynamicRange:      nullText(result.DynamicRange),
		Source:            nullText(result.Source),
		ReleaseGroup:      nullText(result.ReleaseGroup),
		SceneName:         nullText(result.SceneName),
		ClassifiedTitle:   nullText(result.Title),
		ClassifiedYear:    nullInt4(result.Year),
		ClassifiedSeason:  nullInt4(result.Season),
		ClassifiedEpisode: nullInt4(result.Episode),
	}); err != nil {
		log.ErrorContext(ctx, "classify torrent failed", "infohash", infohashHex, "err", err)
		return
	}

	log.InfoContext(ctx, "torrent ingested",
		"infohash", infohashHex,
		"name", info.Name,
		"state", result.State,
		"content_type", result.ContentType,
		"quality", result.Quality,
		"files", len(info.Files),
	)
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

func sanitizePath(path string) string {
	if !utf8.ValidString(path) {
		return strings.ToValidUTF8(path, string(utf8.RuneError))
	}
	return path
}
