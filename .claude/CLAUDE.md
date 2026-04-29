# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**mgnx** is a self-hosted BitTorrent indexer for movies and TV content. It crawls the DHT network using BEP-05 (get_peers) and BEP-51 (sample_infohashes), fetches torrent metadata via BEP-09, classifies torrents by content type, scrapes UDP trackers for seeder/leecher counts, and exposes a Torznab-compatible API (compatible with Prowlarr/Jackett).

## Common Commands

```bash
# Build
go build ./...

# Run server
go run main.go serve

# Run all tests
go test ./...

# Run a single test
go test ./path/to/package -run TestFunctionName

# Generate SQLC query code (after modifying db/schema/queries/*.sql)
sqlc generate

# Validate SQLC config without regenerating
go run main.go validate
```

## Configuration

Viper-based with env prefix `mgnx`. Key config sections and notable fields:

- `database.url` — PostgreSQL connection string (required)
- `dht.node_id` — explicit DHT node ID (mutually exclusive with `dht.external_ip_file`)
- `dht.external_ip_file` / `dht.forwarded_port_file` — Gluetun integration: mgnx reads the VPN-assigned public IP and forwarded port from files written by Gluetun, derives the node ID from the IP, and restarts if those files change
- `server.torznab_port` / `server.health_port` / `server.metrics_port` — three separate HTTP listeners
- `crawler.*` — BEP-51 traversal tuning (workers, alpha, bloom filter rotation, etc.)
- `indexer.*` — metadata fetch concurrency, size/extension filters, adult content exclusion
- `scrape.*` — UDP tracker scrape interval, batch size, dead-torrent detection window

Config file via `--config` flag or `$HOME/.mgnx.yaml`.

## Architecture

### Request / Data Flow

```
DHT Network (UDP)
    │
    ├─ passive: announce_peer → Server.dedup → discovered channel
    └─ active:  BEP-51 sample_infohashes → discoveryQueue → get_peers → discovered channel
                          │
                    indexer.Worker (BEP-09 metadata fetch via TCP)
                          │
                    classify.Classify (regex-based title/quality parsing)
                          │
                    DB: torrents (pending → classified)
                          │
                    scrape.Worker (UDP tracker scrape)
                          │
                    DB: torrents (classified/enriched → active | dead)
                          │
                    torznab.Server HTTP API ← Prowlarr/Sonarr/Radarr
```

### Torrent State Machine

`PENDING → CLASSIFIED → ENRICHED → ACTIVE` (or `REJECTED` / `DEAD`)

The scrape worker drives `CLASSIFIED → ACTIVE` when seeders > 0. `DEAD` is set after a configurable no-seeder window.

### Package Responsibilities

| Package | Role |
|---|---|
| `cmd/` | Cobra CLI; `serve.go` wires all dependencies and starts goroutines via `errgroup` |
| `config/` | `Config` struct; `dht.external_ip_file` and `dht.node_id` are mutually exclusive |
| `dht/` | BEP-05/51 UDP server, Kademlia routing table, crawler, bloom-filter dedup |
| `indexer/` | Consumes `dht.DiscoveredPeers` from crawler, fetches BEP-09 metadata, writes to DB |
| `classify/` | Pure-function regex classifier; extracts title, year, season/episode, quality, encoding, source |
| `scrape/` | UDP tracker scrape loop, adaptive re-scrape scheduling, dead detection, history pruning |
| `metadata/` | BEP-09 extension protocol client (TCP); `Fetcher` interface for mocking |
| `service/` | Torznab search business logic; builds XML RSS responses |
| `torznab/` | HTTP server; single `/api` endpoint dispatches to `service` based on `t=` query param |
| `gluetun/` | Reads IP/port from Gluetun-written files; `WatchFiles` triggers cancel if files change |
| `health/` | `/liveness` and `/readiness` endpoints; readiness checks DB ping + DHT node count > 0 |
| `metrics/` | Prometheus metrics server |
| `recorder/` | Thin wrapper around all Prometheus metrics; no-op when registry is nil (used in tests) |
| `logger/` | `slog`-based structured logger; stored in context via `logger.WithContext` |
| `pkg/cache/` | Generic TTL cache with cleanup callbacks; used to cache BEP-51 node capability |
| `db/` | PostgreSQL connection, migration runner |
| `db/schema/migrations/` | golang-migrate SQL files |
| `db/schema/queries/` | SQLC query sources — **edit these, then run `sqlc generate`** |
| `db/gen/` | Generated SQLC code — do not edit directly |

### DHT Crawler Design

The crawler runs `N` `crawlerInstance` goroutines (BEP-51 active traversal) and `M` `discoveryWorker` goroutines (BEP-05 get_peers fan-out). Each crawler instance maintains two heaps:
- `ready` — nodes ordered by XOR distance, ready to query
- `cooldown` — nodes waiting for their advertised re-query interval

A shared `BloomFilter` (rotating on a configurable interval) deduplicates infohashes across both the passive announce path and the active sample path.

The DHT node ID can be derived from the Gluetun public IP via `dht.DeriveNodeIDFromIP`. If the IP or forwarded port files change on disk, `gluetun.WatchFiles` cancels the root context, triggering a full restart.

### Scrape Scheduling

Adaptive: >100 seeders → 1h, ≥10 → 6h, ≥1 → 24h, 0 → 7 days. Dead detection runs hourly, marking torrents with no seeders for longer than `scrape.dead_after` as `DEAD`.

### Database

PostgreSQL with golang-migrate. Migrations run automatically on startup. `schema.sql` at the repo root is an idempotent bootstrap applied before migrations.

Key tables: `torrents` (central state machine), `torrent_files` (per-file metadata), `trackers`/`torrent_trackers` (normalized tracker URLs), `scrape_history` (90-day retention, pruned by scrape worker), `tmdb_movies`/`tmdb_tv` (enrichment, not yet populated).

### Torznab API

Single endpoint `GET /api` with query param `t=` dispatching to caps, search, movie, or tvsearch handlers. Responses are XML RSS. All search queries filter `state = 'active'` and order by seeders descending.
