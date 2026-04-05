# sqlc skill

Write type-safe PostgreSQL queries for mgnx using sqlc v2 with pgx/v5.

## Config (`sqlc.yaml`)

```yaml
version: "2"
sql:
  - engine: "postgresql"
    schema: "db/migrations/"
    queries: "db/queries/"
    gen:
      go:
        package: "db"
        out: "db/sqlc"
        sql_package: "pgx/v5"
        emit_interface: true
        emit_pointers_for_null_types: true
        emit_json_tags: true
```

## File layout

```
db/
  migrations/          # DDL only (read by sqlc for type info)
  queries/             # One .sql file per domain
    torrents.sql
    torrent_files.sql
    scrape_history.sql
    tmdb_movies.sql
    tmdb_tv.sql
    trackers.sql
  sqlc/                # Generated — never edit by hand
```

## Query annotations

| Annotation    | Use when                               |
|---------------|----------------------------------------|
| `:one`        | SELECT returning 0-1 rows              |
| `:many`       | SELECT returning multiple rows         |
| `:exec`       | INSERT/UPDATE/DELETE, no return needed |
| `:execresult` | Need rows-affected count               |
| `:copyfrom`   | Bulk insert (fastest)                  |
| `:batchexec`  | Batch deletes/updates                  |

## Patterns

### Upsert (infohash PK)

```sql
-- name: UpsertTorrent :one
INSERT INTO torrents (infohash, name, total_size, file_count, state, content_type)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (infohash) DO UPDATE SET
    name        = EXCLUDED.name,
    total_size  = EXCLUDED.total_size,
    last_seen   = now(),
    updated_at  = now()
RETURNING *;
```

### Worker queue (FOR UPDATE SKIP LOCKED)

```sql
-- name: FetchPendingForClassification :many
SELECT * FROM torrents
WHERE state = 'pending'
ORDER BY first_seen ASC
FOR UPDATE SKIP LOCKED
LIMIT $1;
```

Multiple workers call this concurrently — no deadlocks, no double-processing.

### Scrape scheduler

```sql
-- name: FetchDueScrapes :many
SELECT * FROM torrents
WHERE scrape_at < now()
  AND state IN ('classified', 'enriched', 'active')
ORDER BY scrape_at ASC
FOR UPDATE SKIP LOCKED
LIMIT $1;
```

### Array params

```sql
-- name: GetTorrentsByInfohashes :many
SELECT * FROM torrents
WHERE infohash = ANY($1::text[]);
```

Go call: `q.GetTorrentsByInfohashes(ctx, []string{"abc123", "def456"})`

### Bulk insert files (`:copyfrom`)

```sql
-- name: InsertTorrentFiles :copyfrom
INSERT INTO torrent_files (infohash, path, size, extension, is_video)
VALUES ($1, $2, $3, $4, $5);
```

10-100× faster than sequential inserts for file lists.

### Enum-typed params

```sql
-- name: TransitionState :exec
UPDATE torrents SET state = $2, updated_at = now() WHERE infohash = $1;
```

Generated Go uses the `TorrentState` type — state values are compile-time checked.

### Nullable fields

With `emit_pointers_for_null_types: true`, nullable columns become Go pointers:

```go
// tmdb_movies.tmdb_id is nullable → *int32
if movie.TmdbID != nil {
    fmt.Println(*movie.TmdbID)
}
```

### RETURNING on update

```sql
-- name: UpdateSeeders :one
UPDATE torrents
SET seeders = $2, leechers = $3, scrape_at = $4, updated_at = now()
WHERE infohash = $1
RETURNING *;
```

## Rules

- `$1`, `$2`, … positional params only. No `sqlc.narg`, no `COALESCE` tricks.
- Logic belongs in Go, not SQL. Use `ORDER BY` over filter params where possible.
- Use `RETURNING *` on writes when the caller needs the updated row.
- Use `:copyfrom` for bulk file inserts; use `FOR UPDATE SKIP LOCKED` for all worker queues.
- Never hand-edit `db/sqlc/` — always re-run `sqlc generate`.

## Workflow

```bash
# After editing migrations or queries:
sqlc generate
go build ./...
```

Commit `db/sqlc/` to version control. CI should run `sqlc generate && git diff --exit-code db/sqlc/`.
