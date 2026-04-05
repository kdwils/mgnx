-- name: UpsertTracker :one
INSERT INTO trackers (url)
VALUES (sqlc.arg('url'))
ON CONFLICT (url) DO UPDATE SET updated_at = NOW()
RETURNING id, url, created_at, updated_at;

-- name: UpsertTorrentTracker :exec
INSERT INTO torrent_trackers (infohash, tracker_id, last_scrape)
VALUES (sqlc.arg('infohash'), sqlc.arg('tracker_id'), NOW())
ON CONFLICT (infohash, tracker_id) DO UPDATE SET last_scrape = NOW();

-- name: GetTorrentsToScrape :many
SELECT infohash, seeders, state
FROM torrents
WHERE (scrape_at IS NULL OR scrape_at < NOW())
  AND state = ANY(ARRAY['classified', 'enriched', 'active']::torrent_state[])
ORDER BY scrape_at ASC NULLS FIRST
LIMIT sqlc.arg('limit');

-- name: UpdateTorrentScrape :exec
UPDATE torrents
SET
    seeders    = sqlc.arg('seeders'),
    leechers   = sqlc.arg('leechers'),
    scrape_at  = sqlc.arg('scrape_at'),
    state      = sqlc.arg('state'),
    last_seen  = COALESCE(sqlc.narg('last_seen'), last_seen),
    updated_at = NOW()
WHERE infohash = sqlc.arg('infohash');

-- name: InsertScrapeHistory :exec
INSERT INTO scrape_history (infohash, tracker_id, seeders, leechers)
VALUES (
    sqlc.arg('infohash'),
    sqlc.arg('tracker_id'),
    sqlc.arg('seeders'),
    sqlc.arg('leechers')
);

-- name: GetDeadCandidates :many
SELECT infohash
FROM torrents
WHERE seeders = 0
  AND scrape_at IS NOT NULL
  AND state != 'dead'::torrent_state
  AND state != 'rejected'::torrent_state
  AND COALESCE(last_seen, first_seen) < sqlc.arg('cutoff')
LIMIT sqlc.arg('limit');

-- name: UpdateTorrentDead :exec
UPDATE torrents
SET state = 'dead', updated_at = NOW()
WHERE infohash = sqlc.arg('infohash');

-- name: PruneScrapeHistory :exec
DELETE FROM scrape_history
WHERE scraped_at < sqlc.arg('cutoff');
