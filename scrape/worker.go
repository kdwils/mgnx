package scrape

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/kdwils/mgnx/config"
	"github.com/kdwils/mgnx/db/gen"
	"github.com/kdwils/mgnx/logger"
	"github.com/kdwils/mgnx/recorder"
)

// torrentMeta holds the pre-fetched fields needed to process a scrape result.
type torrentMeta struct {
	seeders int64
	state   gen.TorrentState
}

// Worker scrapes a tracker for seeder/leecher counts and maintains torrent state.
type Worker struct {
	queries gen.Querier
	scraper Scraper
	cfg     config.Scrape
	rec     *recorder.Recorder
}

// New creates a Worker.
func New(queries gen.Querier, scraper Scraper, cfg config.Scrape, rec *recorder.Recorder) *Worker {
	return &Worker{queries: queries, scraper: scraper, cfg: cfg, rec: rec}
}

// Run starts the scrape loop, dead detection loop, and prune loop.
// It blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	trackerID := w.initTracker(ctx)

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		w.runScrapeLoop(ctx, trackerID)
	}()

	go func() {
		defer wg.Done()
		w.runDeadDetection(ctx)
	}()

	go func() {
		defer wg.Done()
		w.runPruner(ctx)
	}()

	wg.Wait()
}

// initTracker upserts the primary configured tracker URL and returns its DB ID.
func (w *Worker) initTracker(ctx context.Context) int64 {
	log := logger.FromContext(ctx).With("service", "scraper")
	if len(w.cfg.Trackers) == 0 {
		return 0
	}
	t, err := w.queries.UpsertTracker(ctx, w.cfg.Trackers[0])
	if err != nil {
		log.ErrorContext(ctx, "upsert tracker failed", "url", w.cfg.Trackers[0], "err", err)
		return 0
	}
	return t.ID
}

// runScrapeLoop polls for due torrents and scrapes them.
func (w *Worker) runScrapeLoop(ctx context.Context, trackerID int64) {
	interval := w.cfg.PollInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.scrapeOnce(ctx, trackerID)
		case <-ctx.Done():
			return
		}
	}
}

// scrapeOnce fetches a batch of due torrents and scrapes them.
func (w *Worker) scrapeOnce(ctx context.Context, trackerID int64) {
	log := logger.FromContext(ctx).With("service", "scraper")
	batchSize := w.cfg.BatchSize

	start := time.Now()
	w.rec.IncScrapeCyclesTotal()
	rows, err := w.queries.GetTorrentsToScrape(ctx, int32(batchSize))
	if err != nil {
		log.ErrorContext(ctx, "get torrents to scrape failed", "err", err)
		w.rec.IncScrapeErrorsTotal()
		return
	}
	if len(rows) == 0 {
		return
	}

	log.DebugContext(ctx, "scrape batch started", "count", len(rows))

	infohashes := make([]string, len(rows))
	meta := make(map[string]torrentMeta, len(rows))
	for i, r := range rows {
		infohashes[i] = r.Infohash
		meta[r.Infohash] = torrentMeta{seeders: r.Seeders, state: r.State}
	}

	results, err := w.scraper.Scrape(ctx, infohashes)
	if err != nil {
		log.ErrorContext(ctx, "tracker scrape failed", "err", err)
		w.rec.IncScrapeErrorsTotal()
		return
	}

	for _, r := range results {
		if err := w.applyResult(ctx, r, trackerID, meta[r.Infohash]); err != nil {
			log.ErrorContext(ctx, "apply scrape result failed", "infohash", r.Infohash, "err", err)
			w.rec.IncScrapeErrorsTotal()

		}
	}

	w.rec.ObserveScrapeDurationSeconds(time.Since(start).Seconds())
	w.rec.IncScrapeTorrentsUpdatedTotal()
}

// applyResult writes a single scrape result to the DB.
func (w *Worker) applyResult(ctx context.Context, r ScrapeResult, trackerID int64, m torrentMeta) error {
	seeders := int64(r.Seeders)
	leechers := int64(r.Leechers)

	if trackerID != 0 {
		if err := w.queries.UpsertTorrentTracker(ctx, gen.UpsertTorrentTrackerParams{
			Infohash:  r.Infohash,
			TrackerID: trackerID,
		}); err != nil {
			return err
		}

		if err := w.queries.InsertScrapeHistory(ctx, gen.InsertScrapeHistoryParams{
			Infohash:  r.Infohash,
			TrackerID: trackerID,
			Seeders:   seeders,
			Leechers:  leechers,
		}); err != nil {
			return err
		}
	}

	var lastSeen pgtype.Timestamptz
	if seeders > 0 {
		lastSeen = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	}

	log := logger.FromContext(ctx).With("service", "scraper")
	nextState := newState(m.state, seeders)
	if nextState != m.state {
		log.InfoContext(ctx, "torrent state transition",
			"infohash", r.Infohash,
			"from", m.state,
			"to", nextState,
			"seeders", seeders,
		)
	}
	log.InfoContext(ctx, "torrent scraped",
		"infohash", r.Infohash,
		"seeders", seeders,
		"leechers", leechers,
		"next_scrape", nextScrapeAt(seeders).Round(time.Minute),
	)

	return w.queries.UpdateTorrentScrape(ctx, gen.UpdateTorrentScrapeParams{
		Infohash: r.Infohash,
		Seeders:  seeders,
		Leechers: leechers,
		ScrapeAt: pgtype.Timestamptz{Time: nextScrapeAt(seeders), Valid: true},
		State:    nextState,
		LastSeen: lastSeen,
	})
}

// nextScrapeAt computes the next scrape time using adaptive scheduling.
func nextScrapeAt(seeders int64) time.Time {
	if seeders > 100 {
		return time.Now().Add(time.Hour)
	}
	if seeders >= 10 {
		return time.Now().Add(6 * time.Hour)
	}
	if seeders >= 1 {
		return time.Now().Add(24 * time.Hour)
	}
	return time.Now().Add(7 * 24 * time.Hour)
}

// newState returns the appropriate torrent state after a scrape.
func newState(current gen.TorrentState, seeders int64) gen.TorrentState {
	if seeders > 0 && (current == gen.TorrentStateClassified || current == gen.TorrentStateEnriched) {
		return gen.TorrentStateActive
	}
	return current
}

// runDeadDetection periodically marks zero-seeder torrents as dead after the configured window.
func (w *Worker) runDeadDetection(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.detectDead(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (w *Worker) detectDead(ctx context.Context) {
	log := logger.FromContext(ctx).With("service", "scraper")

	deadAfter := w.cfg.DeadAfter
	if deadAfter <= 0 {
		deadAfter = 90 * 24 * time.Hour
	}

	cutoff := pgtype.Timestamptz{Time: time.Now().Add(-deadAfter), Valid: true}
	candidates, err := w.queries.GetDeadCandidates(ctx, gen.GetDeadCandidatesParams{
		Cutoff: cutoff,
		Limit:  1000,
	})
	if err != nil {
		log.ErrorContext(ctx, "get dead candidates failed", "err", err)
		return
	}

	log.DebugContext(ctx, "dead detection run", "candidates", len(candidates))
	deadCount := 0
	for _, infohash := range candidates {
		if err := w.queries.UpdateTorrentDead(ctx, infohash); err != nil {
			log.ErrorContext(ctx, "mark torrent dead failed", "infohash", infohash, "err", err)
			continue
		}
		deadCount++
		log.DebugContext(ctx, "torrent marked dead", "infohash", infohash)
	}
	if deadCount > 0 {
		w.rec.IncScrapeDeadDetectedTotal()
	}
}

// runPruner periodically deletes scrape_history older than 90 days.
func (w *Worker) runPruner(ctx context.Context) {
	interval := w.cfg.PruneInterval
	if interval <= 0 {
		interval = 24 * time.Hour
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.prune(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (w *Worker) prune(ctx context.Context) {
	log := logger.FromContext(ctx).With("service", "scraper")
	cutoff := pgtype.Timestamptz{Time: time.Now().Add(-90 * 24 * time.Hour), Valid: true}
	if err := w.queries.PruneScrapeHistory(ctx, cutoff); err != nil {
		log.ErrorContext(ctx, "prune scrape history failed", "err", err)
	}
	w.rec.IncScrapeHistoryPrunedTotal()
}
