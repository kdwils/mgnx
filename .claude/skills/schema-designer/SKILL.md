# schema-designer skill

Design normalized PostgreSQL schemas for mgnx. Eliminate duplication first, index for the queries you actually have.

## Normalization targets

Reach **3NF by default**. Go to BCNF only when anomalies are demonstrable. Denormalize only with a measured query justification — and document it.

| Normal Form | Rule |
|-------------|------|
| 1NF | Atomic values. No arrays of structured data, no comma-separated lists in a column. |
| 2NF | Every non-key column depends on the *whole* primary key (matters for composite PKs). |
| 3NF | No transitive dependencies — non-key columns must depend on the key, not on each other. |

**Red flags that signal a normalization miss:**
- A column that duplicates data already in another table (e.g. `torrent_files.torrent_name`)
- An enum-like column that grows (use a lookup table or PG enum type instead)
- A nullable FK that should be its own table (`tmdb_movie_id` on `torrents` → separate `torrent_enrichments` table)
- Repeated groups of columns (`tag1`, `tag2`, `tag3` → normalize to a join table)

## Primary keys

- Use `TEXT` natural keys when the domain gives you a stable unique identifier (e.g. `infohash`, `imdb_id`). No UUID overhead, no surrogate needed.
- Use `BIGINT GENERATED ALWAYS AS IDENTITY` when no natural key exists. Never `SERIAL`.
- Composite PKs are fine for pure join tables (`torrent_trackers(infohash, tracker_url)`).

```sql
-- Natural key
CREATE TABLE torrents (
    infohash TEXT PRIMARY KEY,
    ...
);

-- Surrogate key
CREATE TABLE trackers (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    url TEXT NOT NULL UNIQUE,
    ...
);

-- Join table
CREATE TABLE torrent_trackers (
    infohash    TEXT NOT NULL REFERENCES torrents(infohash) ON DELETE CASCADE,
    tracker_id  BIGINT NOT NULL REFERENCES trackers(id) ON DELETE CASCADE,
    PRIMARY KEY (infohash, tracker_id)
);
```

## Relationships

### One-to-many
FK on the "many" side. Always include `ON DELETE` behavior — never omit it.

```sql
CREATE TABLE torrent_files (
    id       BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    infohash TEXT NOT NULL REFERENCES torrents(infohash) ON DELETE CASCADE,
    path     TEXT NOT NULL,
    size     BIGINT NOT NULL,
    ...
);
```

### Many-to-many
Always via a join table. Store relationship metadata on the join table, not on either entity.

```sql
-- relationship metadata (announce_url, last_announce) lives on the join row
CREATE TABLE torrent_trackers (
    infohash      TEXT   NOT NULL REFERENCES torrents(infohash)  ON DELETE CASCADE,
    tracker_id    BIGINT NOT NULL REFERENCES trackers(id)        ON DELETE CASCADE,
    last_announce TIMESTAMPTZ,
    PRIMARY KEY (infohash, tracker_id)
);
```

### One-to-one (optional association)
Use a separate table with a `UNIQUE` FK rather than nullable columns on the parent. Keeps the parent table lean and the association queryable independently.

```sql
-- instead of: ALTER TABLE torrents ADD COLUMN tmdb_id INT;
CREATE TABLE torrent_tmdb (
    infohash TEXT    PRIMARY KEY REFERENCES torrents(infohash) ON DELETE CASCADE,
    tmdb_id  INTEGER NOT NULL,
    kind     TEXT    NOT NULL CHECK (kind IN ('movie', 'tv'))
);
```

## Data types

| Use case | Type |
|----------|------|
| Timestamps | `TIMESTAMPTZ` — always with timezone |
| State machine | `TEXT` with `CHECK` or a named PG `ENUM` |
| File sizes, counters | `BIGINT` |
| Ratios, rates | `NUMERIC(p,s)` or `DOUBLE PRECISION` |
| Flags | `BOOLEAN NOT NULL DEFAULT false` |
| Short bounded strings | `TEXT` with a `CHECK (char_length(col) <= N)` |

Prefer `TEXT` over `VARCHAR(n)` — Postgres stores them identically; `TEXT` avoids arbitrary length churn.

Named enums for stable, closed sets:
```sql
CREATE TYPE torrent_state AS ENUM ('pending', 'classified', 'enriched', 'active', 'dead');
ALTER TABLE torrents ADD COLUMN state torrent_state NOT NULL DEFAULT 'pending';
```

## Constraints

Enforce invariants in the database, not just in application code.

```sql
-- Non-null by default unless absence is meaningful
name TEXT NOT NULL,

-- Uniqueness
UNIQUE (infohash, tracker_id),

-- Value bounds
CHECK (seeders >= 0),
CHECK (kind IN ('movie', 'tv')),         -- for text enums
CHECK (char_length(name) <= 500),

-- Timestamps
created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
```

Always add `created_at` and `updated_at` to every entity table.

## Indexes

Index for queries you have written, not speculatively. Add one index at a time, explain the query that justifies it.

```sql
-- Covering index for worker queue (avoids heap fetch on state filter + order)
CREATE INDEX idx_torrents_state_first_seen ON torrents (state, first_seen ASC)
    WHERE state = 'pending';

-- FK index (Postgres does NOT auto-create these)
CREATE INDEX idx_torrent_files_infohash ON torrent_files (infohash);

-- Partial index for scrape scheduler
CREATE INDEX idx_torrents_scrape_at ON torrents (scrape_at ASC)
    WHERE state IN ('classified', 'enriched', 'active');

-- GIN for full-text search
CREATE INDEX idx_torrents_name_fts ON torrents USING GIN (to_tsvector('english', name));
```

**Rules:**
- Every FK column needs an explicit index — Postgres does not create them automatically.
- Partial indexes (`WHERE`) are often better than full indexes for queues and filtered lookups.
- Composite index column order: equality filters first, then range/order columns.
- Do not index low-cardinality boolean columns alone — combine with a higher-cardinality column.

## Audit / soft-delete

For audit trails, prefer an `_history` table with a trigger rather than `deleted_at` on the main table. Soft-delete via `deleted_at` complicates every query and unique constraint.

If soft-delete is truly required, use a partial unique index:
```sql
CREATE UNIQUE INDEX idx_torrents_infohash_active
    ON torrents (infohash)
    WHERE deleted_at IS NULL;
```

## Schema checklist before writing migrations

- [ ] Every entity has `created_at TIMESTAMPTZ NOT NULL DEFAULT now()` and `updated_at`
- [ ] Every FK has a matching index
- [ ] Every FK has an explicit `ON DELETE` clause
- [ ] No nullable columns that should be their own table
- [ ] No repeated column groups (denormalized arrays → join table)
- [ ] Enum-like text columns use PG `ENUM` type or `CHECK` constraint
- [ ] Constraints enforce invariants (non-null, range checks, uniqueness)
- [ ] Partial indexes added for all filtered worker-queue queries
- [ ] No column duplicates data already in a joined table

## Migration file naming

Follows golang-migrate convention — pair each change:
```
db/migrations/
  000001_create_torrents.up.sql
  000001_create_torrents.down.sql
  000002_create_torrent_files.up.sql
  000002_create_torrent_files.down.sql
```

One logical change per migration. Never mix DDL (schema) and DML (data backfills) in the same migration file.
