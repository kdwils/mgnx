-- name: ListTorrents :many
SELECT
    infohash,
    name,
    total_size,
    file_count,
    state,
    content_type,
    quality,
    encoding,
    dynamic_range,
    source,
    release_group,
    seeders,
    leechers,
    first_seen,
    created_at,
    updated_at
FROM torrents
WHERE
    (sqlc.narg('state')::torrent_state IS NULL OR state = sqlc.narg('state')::torrent_state)
    AND (sqlc.narg('content_type')::content_type IS NULL OR content_type = sqlc.narg('content_type')::content_type)
ORDER BY created_at DESC
LIMIT sqlc.arg('page_size')
OFFSET sqlc.arg('page_offset');

-- name: DeleteTorrent :exec
DELETE FROM torrents WHERE infohash = sqlc.arg('infohash');

-- name: UpdateTorrentState :exec
UPDATE torrents
SET state = sqlc.arg('state'), updated_at = NOW()
WHERE infohash = sqlc.arg('infohash');
