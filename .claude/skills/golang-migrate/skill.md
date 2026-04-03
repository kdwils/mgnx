# golang-migrate skill

Write and manage PostgreSQL migrations for magnetite using golang-migrate v4.
Migrations run automatically on server start via embedded SQL files.

## File naming

```
db/migrations/
  000001_init_schema.up.sql
  000001_init_schema.down.sql
  000002_add_indexes.up.sql
  000002_add_indexes.down.sql
```

- Zero-padded 6-digit sequence: `000001`, `000002`, …
- Description uses underscores, lowercase
- Every `.up.sql` must have a matching `.down.sql`

## Go integration

Migrations are embedded and run on server start:

```go
// db/migrate.go
package db

import (
    "embed"
    "errors"
    "fmt"

    migrate "github.com/golang-migrate/migrate/v4"
    "github.com/golang-migrate/migrate/v4/database/postgres"
    "github.com/golang-migrate/migrate/v4/source/iofs"
    "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var migrations embed.FS

func RunMigrations(pool *pgxpool.Pool) error {
    db := stdlib.OpenDBFromPool(pool)

    src, err := iofs.New(migrations, "migrations")
    if err != nil {
        return fmt.Errorf("migration source: %w", err)
    }

    driver, err := postgres.WithInstance(db, &postgres.Config{})
    if err != nil {
        return fmt.Errorf("migration driver: %w", err)
    }

    m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
    if err != nil {
        return fmt.Errorf("migration init: %w", err)
    }

    if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
        return fmt.Errorf("migration up: %w", err)
    }

    return nil
}
```

Call `db.RunMigrations(pool)` before the server starts accepting requests.

## DDL conventions

### Always use IF NOT EXISTS / IF EXISTS

```sql
-- up
CREATE TABLE IF NOT EXISTS torrents ( ... );

-- down
DROP TABLE IF EXISTS torrents;
```

### Enums: create type before table, drop after table

```sql
-- up
CREATE TYPE torrent_state AS ENUM (
    'pending', 'classified', 'enriched', 'active', 'dead', 'rejected'
);

CREATE TYPE content_type AS ENUM ('movie', 'tv', 'unknown');

CREATE TABLE IF NOT EXISTS torrents (
    infohash    text PRIMARY KEY,
    state       torrent_state NOT NULL DEFAULT 'pending',
    content_type content_type NOT NULL DEFAULT 'unknown',
    ...
);

-- down
DROP TABLE IF EXISTS torrents;
DROP TYPE IF EXISTS content_type;
DROP TYPE IF EXISTS torrent_state;
```

### Indexes: use CONCURRENTLY, split from table creation

Add indexes in their own migration (or after the CREATE TABLE block) so `CONCURRENTLY` works:

```sql
-- up
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_torrents_state
    ON torrents(state);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_torrents_scrape_at
    ON torrents(scrape_at)
    WHERE state IN ('classified', 'enriched', 'active');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_torrents_name_gin
    ON torrents USING GIN(to_tsvector('english', name));

-- down
DROP INDEX CONCURRENTLY IF EXISTS idx_torrents_name_gin;
DROP INDEX CONCURRENTLY IF EXISTS idx_torrents_scrape_at;
DROP INDEX CONCURRENTLY IF EXISTS idx_torrents_state;
```

### Foreign keys with CASCADE

```sql
CREATE TABLE IF NOT EXISTS torrent_files (
    id          bigserial PRIMARY KEY,
    infohash    text NOT NULL REFERENCES torrents(infohash) ON DELETE CASCADE,
    path        text NOT NULL,
    size        bigint NOT NULL,
    extension   text NOT NULL,
    is_video    boolean NOT NULL DEFAULT false
);
```

### Timestamps

Use `timestamptz` everywhere:

```sql
created_at  timestamptz NOT NULL DEFAULT now(),
updated_at  timestamptz NOT NULL DEFAULT now()
```

### Adding a column (new migration)

```sql
-- up
ALTER TABLE torrents ADD COLUMN IF NOT EXISTS scene_name text;

-- down
ALTER TABLE torrents DROP COLUMN IF EXISTS scene_name;
```

## Dirty state recovery

If a migration fails mid-flight, the DB is marked dirty:

```
Error: Dirty database version 3. Fix and force version.
```

Fix by correcting the migration SQL, then forcing the version back:

```bash
# Force to version 2 (last clean version) to allow re-run
migrate -path db/migrations -database "$DATABASE_URL" force 2
```

Or programmatically (use with care):

```go
m.Force(2)
m.Up()
```

## Rules

- Never edit a migration that has already been applied in production. Write a new one.
- One logical change per migration. Don't bundle table creation with index creation.
- Always write the `.down.sql`. Even if rolling back is rare, it enables local dev resets.
- `CREATE INDEX CONCURRENTLY` cannot run inside a transaction — omit `BEGIN`/`COMMIT` in that migration file.
- Enum changes (adding a value) require `ALTER TYPE ... ADD VALUE` — these are irreversible without a new type.

## Packages required

```bash
go get github.com/golang-migrate/migrate/v4
go get github.com/golang-migrate/migrate/v4/database/postgres
go get github.com/golang-migrate/migrate/v4/source/iofs
```
