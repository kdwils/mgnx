# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**Magnetite** is a self-hosted BitTorrent indexer for movies and TV content. It crawls the DHT network, classifies torrents, enriches metadata via the TMDB API, and exposes a Torznab-compatible API (compatible with Prowlarr/Jackett).

## Common Commands

```bash
# Build
go build ./...

# Run server
go run main.go serve

# Run tests
go test ./...

# Run a single test
go test ./path/to/package -run TestFunctionName

# Generate SQLC query code (after modifying db/schema/queries/*.sql)
sqlc generate

# Apply database migrations (handled automatically on startup)
```

## Configuration

Viper-based configuration with env prefix `MAGNETITE`. Key environment variables:
- `MAGNETITE_DATABASE_URL` — PostgreSQL connection string (required)

Config file support via `--config` flag or `$HOME/.magnetite.yaml`.

## Architecture

### Request / Data Flow

```
DHT Network → crawler → torrents (pending)
                          ↓
                    classifier → torrents (classified)
                          ↓
                    TMDB enricher → torrents (enriched/active)
                          ↓
                    UDP tracker scrape → seeder/leecher counts
                          ↓
                    Torznab HTTP API ← Prowlarr/Sonarr/Radarr
```

### Torrent State Machine

`PENDING → CLASSIFIED → ENRICHED → ACTIVE` (or `DEAD` / `REJECTED`)

### Package Responsibilities

| Package | Role |
|---|---|
| `cmd/` | Cobra CLI commands; `root.go` binds config, `serve.go` wires dependencies and starts the server |
| `config/` | `Config` struct + `New()` to unmarshal viper config |
| `db/` | PostgreSQL connection (`connect.go`), migration runner (`migrate.go`), embedded migration files |
| `db/schema/migrations/` | Raw SQL migration files (golang-migrate, numbered sequence) |
| `db/schema/queries/` | SQLC query source files — **edit these, then run `sqlc generate`** |
| `db/queries/` | **Generated** SQLC code — do not edit directly |
| `server/` | HTTP server and middleware (not yet implemented) |

### Database

PostgreSQL with golang-migrate for schema migrations. Key tables:
- `torrents` — central table; holds state machine, quality metadata, seeder/leecher counts
- `torrent_files` — per-file metadata used during classification
- `trackers` / `torrent_trackers` — normalized tracker URLs
- `scrape_history` — point-in-time seeder/leecher snapshots (90-day retention)
- `tmdb_movies` / `tmdb_tv` — TMDB enrichment data
- `genres` / join tables — normalized genre lookup

`schema.sql` at the repo root is an idempotent bootstrap schema applied before migrations run.

### SQLC Workflow

Query definitions live in `db/schema/queries/`. After editing them:
1. Run `sqlc generate`
2. Commit the regenerated files under `db/queries/`

All Torznab search queries filter to `state = 'active'` and rank by seeders descending.

## Project Status

Early stage. Foundation complete (schema, migrations, SQLC, CLI scaffold). Not yet implemented: DHT crawler, classification pipeline, TMDB enrichment worker, UDP tracker scraping, Torznab HTTP endpoints, structured logging, metrics. See `PLAN.md` for the full implementation roadmap.
