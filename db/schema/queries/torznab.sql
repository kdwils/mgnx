-- name: SearchAll :many
-- General search across all active torrents. Used for t=search.
SELECT
    t.infohash,
    t.name,
    t.total_size,
    t.file_count,
    t.content_type,
    t.quality,
    t.encoding,
    t.dynamic_range,
    t.source,
    t.release_group,
    t.seeders,
    t.leechers,
    t.first_seen
FROM torrents t
WHERE t.state = 'active'
  AND (sqlc.narg('query')::text IS NULL
       OR to_tsvector('english', t.name) @@ plainto_tsquery('english', sqlc.narg('query')::text))
ORDER BY t.seeders DESC
LIMIT sqlc.arg('page_size')
OFFSET sqlc.arg('page_offset');

-- name: SearchMovies :many
-- Search active movie torrents with enrichment data. Used for t=movie.
SELECT
    t.infohash,
    t.name,
    t.total_size,
    t.file_count,
    t.quality,
    t.encoding,
    t.dynamic_range,
    t.source,
    t.release_group,
    t.seeders,
    t.leechers,
    t.first_seen,
    m.tmdb_id,
    m.imdb_id,
    m.title        AS movie_title,
    m.year,
    m.overview,
    m.runtime,
    m.vote_average,
    m.poster_path
FROM torrents t
JOIN tmdb_movies m ON m.infohash = t.infohash
WHERE t.state = 'active'
  AND (sqlc.narg('query')::text IS NULL
       OR to_tsvector('english', t.name) @@ plainto_tsquery('english', sqlc.narg('query')::text))
ORDER BY t.seeders DESC
LIMIT sqlc.arg('page_size')
OFFSET sqlc.arg('page_offset');

-- name: GetMoviesByIMDB :many
-- Fetch all active encodes of a specific movie by IMDB ID. Used for t=movie&imdbid=.
SELECT
    t.infohash,
    t.name,
    t.total_size,
    t.file_count,
    t.quality,
    t.encoding,
    t.dynamic_range,
    t.source,
    t.release_group,
    t.seeders,
    t.leechers,
    t.first_seen,
    m.tmdb_id,
    m.imdb_id,
    m.title        AS movie_title,
    m.year,
    m.overview,
    m.runtime,
    m.vote_average,
    m.poster_path
FROM torrents t
JOIN tmdb_movies m ON m.infohash = t.infohash
WHERE t.state = 'active'
  AND m.imdb_id = sqlc.arg('imdb_id')
ORDER BY t.seeders DESC;

-- name: SearchTV :many
-- Search active TV torrents with enrichment data. Used for t=tvsearch.
SELECT
    t.infohash,
    t.name,
    t.total_size,
    t.file_count,
    t.quality,
    t.encoding,
    t.dynamic_range,
    t.source,
    t.release_group,
    t.seeders,
    t.leechers,
    t.first_seen,
    tv.tmdb_id,
    tv.imdb_id,
    tv.series_name,
    tv.season,
    tv.episode,
    tv.episode_name,
    tv.first_air_date
FROM torrents t
JOIN tmdb_tv tv ON tv.infohash = t.infohash
WHERE t.state = 'active'
  AND (sqlc.narg('query')::text IS NULL
       OR to_tsvector('english', t.name) @@ plainto_tsquery('english', sqlc.narg('query')::text))
  AND (sqlc.narg('season')::int IS NULL OR tv.season = sqlc.narg('season')::int)
  AND (sqlc.narg('episode')::int IS NULL OR tv.episode = sqlc.narg('episode')::int)
ORDER BY t.seeders DESC
LIMIT sqlc.arg('page_size')
OFFSET sqlc.arg('page_offset');

-- name: GetTVByIMDB :many
-- Fetch TV torrents by IMDB ID, optionally filtered to season/episode. Used for t=tvsearch&imdbid=.
SELECT
    t.infohash,
    t.name,
    t.total_size,
    t.file_count,
    t.quality,
    t.encoding,
    t.dynamic_range,
    t.source,
    t.release_group,
    t.seeders,
    t.leechers,
    t.first_seen,
    tv.tmdb_id,
    tv.imdb_id,
    tv.series_name,
    tv.season,
    tv.episode,
    tv.episode_name,
    tv.first_air_date
FROM torrents t
JOIN tmdb_tv tv ON tv.infohash = t.infohash
WHERE t.state = 'active'
  AND tv.imdb_id = sqlc.arg('imdb_id')
  AND (sqlc.narg('season')::int IS NULL OR tv.season = sqlc.narg('season')::int)
  AND (sqlc.narg('episode')::int IS NULL OR tv.episode = sqlc.narg('episode')::int)
ORDER BY t.seeders DESC;

-- name: GetTorrentByInfohash :one
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
WHERE infohash = sqlc.arg('infohash');
