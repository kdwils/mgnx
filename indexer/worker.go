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

	"github.com/jackc/pgx/v5"
	"github.com/kdwils/mgnx/classify"
	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/db/gen"
	"github.com/kdwils/mgnx/dht/types"
	"github.com/kdwils/mgnx/logger"
	"github.com/kdwils/mgnx/metadata"
	"github.com/kdwils/mgnx/recorder"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

// Worker handles the metadata fetching and classification of discovered torrents.
type Worker struct {
	ch                  <-chan types.DiscoveredPeers
	fetcher             metadata.Fetcher
	queries             gen.Querier
	rec                 *recorder.Recorder
	allowedExts         map[string]struct{}
	peerTimeout         time.Duration
	maxPeers            int
	maxConcurrentPeers  int
	rateLimiter         *rate.Limiter
	minSize             int64
	maxSize             int64
	workers             int
	enableExtFilter     bool
	excludeAdultContent bool
}

func New(ch <-chan types.DiscoveredPeers, fetcher metadata.Fetcher, queries gen.Querier, cfg config.Indexer, rec *recorder.Recorder) *Worker {
	return &Worker{
		ch:                  ch,
		fetcher:             fetcher,
		queries:             queries,
		rec:                 rec,
		allowedExts:         allowedExtensions(cfg.AllowedExtensions),
		peerTimeout:         cfg.RequestTimeout,
		maxPeers:            cfg.MaxPeers,
		maxConcurrentPeers:  cfg.MaxConcurrentPeers,
		rateLimiter:         rate.NewLimiter(rate.Limit(cfg.RateLimit), cfg.RateBurst),
		minSize:             cfg.MinSize,
		maxSize:             cfg.MaxSize,
		workers:             cfg.Workers,
		enableExtFilter:     cfg.EnableExtensionFilter,
		excludeAdultContent: cfg.ExcludeAdultContent,
	}
}

func (w *Worker) Run(ctx context.Context) {
	var wg sync.WaitGroup
	w.rec.SetIndexerWorkersActive(float64(w.workers))
	for range w.workers {
		wg.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-w.ch:
					if !ok {
						return
					}
					w.rec.SetDiscoveredChannelDepth(float64(len(w.ch)))
					w.rec.AddIndexerWorkersBusy(1)
					w.process(ctx, ev)
					w.rec.AddIndexerWorkersBusy(-1)
				}
			}
		})
	}
	wg.Wait()
}

func (w *Worker) process(ctx context.Context, ev types.DiscoveredPeers) {
	log := logger.FromContext(ctx)
	infohashHex := hex.EncodeToString(ev.Infohash[:])

	log.DebugContext(ctx, "processing infohash", "infohash", infohashHex, "peers", len(ev.Peers))

	lookupStart := time.Now()
	_, err := w.queries.GetTorrentByInfohash(ctx, infohashHex)
	w.rec.ObserveIndexerDBQueryDurationSeconds("lookup", time.Since(lookupStart).Seconds())
	if err == nil {
		w.rec.IncIndexerPeersProcessedTotal("duplicate")
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		log.ErrorContext(ctx, "db lookup failed", "infohash", infohashHex, "err", err)
		w.rec.IncIndexerPeersProcessedTotal("error")
		return
	}

	w.rec.IncIndexerPeersProcessedTotal("processed")
	w.rec.ObserveIndexerPeersPerInfohash(float64(len(ev.Peers)))

	maxPeers := min(len(ev.Peers), w.maxPeers)

	var info *metadata.TorrentInfo

	if maxPeers == 0 {
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(w.maxConcurrentPeers)

	resultCh := make(chan *metadata.TorrentInfo, 1)

	for i := range maxPeers {
		rlStart := time.Now()
		if err := w.rateLimiter.Wait(gctx); err != nil {
			break
		}
		w.rec.ObserveIndexerRateLimiterWaitSeconds(time.Since(rlStart).Seconds())
		peer := ev.Peers[i]
		g.Go(func() error {
			fetchCtx, fetchCancel := context.WithTimeout(gctx, w.peerTimeout)
			defer fetchCancel()
			addr := net.TCPAddr{IP: peer.SourceIP, Port: peer.Port}
			info, err := w.fetcher.Fetch(fetchCtx, ev.Infohash, addr)
			if err != nil {
				return nil
			}
			select {
			case resultCh <- info:
			default:
			}
			return nil
		})
	}

	go func() {
		g.Wait()
		close(resultCh)
	}()

	start := time.Now()
	info = <-resultCh
	if info == nil {
		log.DebugContext(ctx, "no metadata fetched", "infohash", infohashHex)
		w.rec.IncIndexerMetadataFailedTotal("no_peers")
		return
	}

	w.rec.ObserveIndexerFetchDurationSeconds(time.Since(start).Seconds())
	w.rec.IncIndexerMetadataFetchedTotal()

	log.DebugContext(ctx, "metadata fetched", "infohash", infohashHex, "name", info.Name, "files", len(info.Files))

	info.Name = strings.ToValidUTF8(info.Name, "")

	classifyFiles := make([]classify.File, len(info.Files))
	for i, f := range info.Files {
		classifyFiles[i] = classify.File{Path: strings.ToValidUTF8(f.Path, ""), Size: f.Size}
	}

	if classify.ContainsBlockedContent(info.Name, classifyFiles) {
		log.DebugContext(ctx, "dropping blocked torrent", "infohash", infohashHex)
		w.rec.IncTorrentsRejectedTotal("blocked_content")
		return
	}

	upsertStart := time.Now()
	tag, err := w.queries.UpsertTorrentPending(ctx, gen.UpsertTorrentPendingParams{
		Infohash:  infohashHex,
		Name:      info.Name,
		TotalSize: info.TotalSize,
		FileCount: int64(len(info.Files)),
	})
	w.rec.ObserveIndexerDBQueryDurationSeconds("upsert", time.Since(upsertStart).Seconds())
	if err != nil {
		log.ErrorContext(ctx, "upsert torrent failed", "infohash", infohashHex, "err", err)
		w.rec.IncIndexerDBErrorsTotal("upsert")
		return
	}

	if tag.RowsAffected() == 0 {
		return
	}
	w.rec.IncIndexerDBUpsertsTotal()
	infohashes := make([]string, len(info.Files))
	paths := make([]string, len(info.Files))
	sizes := make([]int64, len(info.Files))
	extensions := make([]string, len(info.Files))
	isVideos := make([]bool, len(info.Files))

	for i, f := range classifyFiles {
		ext := filepath.Ext(f.Path)
		infohashes[i] = infohashHex
		paths[i] = f.Path
		sizes[i] = f.Size
		extensions[i] = ext
		isVideos[i] = classify.IsVideoExt(ext)
	}

	insertFilesStart := time.Now()
	err = w.queries.InsertTorrentFiles(ctx, gen.InsertTorrentFilesParams{
		Infohash:  infohashes,
		Path:      paths,
		Size:      sizes,
		Extension: extensions,
		IsVideo:   isVideos,
	})
	w.rec.ObserveIndexerDBQueryDurationSeconds("insert_files", time.Since(insertFilesStart).Seconds())
	if err != nil {
		log.ErrorContext(ctx, "insert torrent files failed", "infohash", infohashHex, "err", err)
		w.rec.IncIndexerDBErrorsTotal("insert_files")
		return
	}

	result := classify.Classify(info.Name, classifyFiles, info.TotalSize, w.minSize, w.maxSize, w.allowedExts, w.enableExtFilter, w.excludeAdultContent)
	log.DebugContext(ctx, "classified torrent", "infohash", infohashHex, "state", result.State, "content_type", result.ContentType)
	if result.RejectionReason != "" {
		w.rec.IncTorrentsRejectedTotal(result.RejectionReason)
	}
	w.rec.IncTorrentsIndexedTotal(string(result.ContentType))

	classifyStart := time.Now()
	err = w.queries.UpdateTorrentClassified(ctx, classifyParams(infohashHex, result.State, result))
	w.rec.ObserveIndexerDBQueryDurationSeconds("classify_update", time.Since(classifyStart).Seconds())
	if err != nil {
		log.ErrorContext(ctx, "classify torrent failed", "infohash", infohashHex, "err", err)
		w.rec.IncIndexerDBErrorsTotal("update_classified")
		return
	}

	log.InfoContext(ctx, "torrent indexed",
		"infohash", infohashHex,
		"name", info.Name,
		"state", result.State,
		"content_type", result.ContentType,
		"quality", result.Quality,
		"files", len(info.Files),
	)
}
