-- name: UpsertTorrentPending :execresult
INSERT INTO torrents (infohash, name, total_size, file_count)
VALUES (
    sqlc.arg('infohash'),
    sqlc.arg('name'),
    sqlc.arg('total_size'),
    sqlc.arg('file_count')
)
ON CONFLICT (infohash) DO NOTHING;

-- name: InsertTorrentFiles :exec
INSERT INTO torrent_files (infohash, path, size, extension, is_video)
SELECT
    unnest(sqlc.arg('infohash')::text[]),
    unnest(sqlc.arg('path')::text[]),
    unnest(sqlc.arg('size')::bigint[]),
    nullif(unnest(sqlc.arg('extension')::text[]), ''),
    unnest(sqlc.arg('is_video')::boolean[]);

-- name: CountTorrents :one
SELECT COUNT(*) FROM torrents;

-- name: GetTorrentsToIndex :many
SELECT infohash, name, total_size, state, content_type,
       classified_title, classified_season, classified_episode
FROM torrents
ORDER BY infohash ASC
LIMIT sqlc.arg('limit')
OFFSET sqlc.arg('offset');

-- name: GetTorrentFiles :many
SELECT path, size
FROM torrent_files
WHERE infohash = sqlc.arg('infohash');

-- name: UpdateTorrentClassified :exec
UPDATE torrents
SET
    state              = sqlc.arg('state'),
    content_type       = sqlc.arg('content_type'),
    quality            = sqlc.narg('quality'),
    encoding           = sqlc.narg('encoding'),
    dynamic_range      = sqlc.narg('dynamic_range'),
    source             = sqlc.narg('source'),
    release_group      = sqlc.narg('release_group'),
    scene_name         = sqlc.narg('scene_name'),
    classified_title   = sqlc.narg('classified_title'),
    classified_year    = sqlc.narg('classified_year'),
    classified_season  = sqlc.narg('classified_season'),
    classified_episode = sqlc.narg('classified_episode'),
    updated_at         = NOW()
WHERE infohash = sqlc.arg('infohash');