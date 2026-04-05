-- name: UpsertTorrentPending :execresult
INSERT INTO torrents (infohash, name, total_size, file_count)
VALUES (
    sqlc.arg('infohash'),
    sqlc.arg('name'),
    sqlc.arg('total_size'),
    sqlc.arg('file_count')
)
ON CONFLICT (infohash) DO NOTHING;

-- name: InsertTorrentFile :exec
INSERT INTO torrent_files (infohash, path, size, extension, is_video)
VALUES (
    sqlc.arg('infohash'),
    sqlc.arg('path'),
    sqlc.arg('size'),
    sqlc.narg('extension'),
    sqlc.arg('is_video')
);

-- name: UpdateTorrentClassified :exec
UPDATE torrents
SET
    state         = sqlc.arg('state'),
    content_type  = sqlc.arg('content_type'),
    quality       = sqlc.narg('quality'),
    encoding      = sqlc.narg('encoding'),
    dynamic_range = sqlc.narg('dynamic_range'),
    source        = sqlc.narg('source'),
    release_group = sqlc.narg('release_group'),
    scene_name    = sqlc.narg('scene_name'),
    updated_at    = NOW()
WHERE infohash = sqlc.arg('infohash');
