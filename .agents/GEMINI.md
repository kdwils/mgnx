# mgnx - BitTorrent DHT Crawler & Indexer

`mgnx` is a high-performance BitTorrent DHT crawler and metadata indexer. It traverses the DHT network to discover torrents, fetches their metadata, and provides multiple interfaces for searching and browsing.

## Project Overview

### Architecture

The system is composed of several concurrent services managed by an `errgroup`:

- **DHT Server (`dht/`)**: Handles the core KRPC protocol (BEP-5), routing table management, and peer discovery.
- **DHT Crawler (`dht/crawler.go`)**: Traverses the DHT using `sample_infohashes` (BEP-51) to discover new infohashes.
- **Discovery Workers (`dht/discovery.go`)**: Processes discovered infohashes to find active peers.
- **Metadata Indexer (`indexer/`)**: Fetches metadata (name, files, size) from discovered peers using the extension protocol (BEP-9).
- **Scraper (`scrape/`)**: Periodically updates seeder and leecher counts by querying BitTorrent trackers.
- **API Server (`api/`, `torznab/`)**: Provides a RESTful API and a Torznab-compatible XML interface for integration with tools like Prowlarr, Sonarr, and Radarr.
- **TUI (`tui/`)**: A Terminal User Interface built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) for interactive searching.
- **Database (`db/`)**: PostgreSQL backend for persistent storage. Uses `sqlc` for code generation from SQL and `golang-migrate` for schema management.

### Key Technologies

- **Language**: Go 1.26
- **Database**: PostgreSQL (via `pgx`)
- **CLI**: `cobra`, `viper`
- **TUI**: `bubbletea`, `lipgloss`
- **Metrics**: `prometheus`
- **Networking**: Custom UDP implementation for DHT, `anacrolix/torrent` for metadata fetching.

## Building and Running

### Prerequisites

- Go 1.26+
- PostgreSQL
- (Optional) [sqlc](https://sqlc.dev/) for database code generation.
- (Optional) [golang-migrate](https://github.com/golang-migrate/migrate) for manual migration management.

### Configuration

Configuration is managed via `viper`. By default, it looks for a `config.yaml` in the current directory or environment variables. See `config/config.go` for available options.

### Commands

- **Build**: `go build -o mgnx main.go`
- **Serve (Start Crawler/Indexer/API)**: `./mgnx serve`
- **TUI (Search UI)**: `./mgnx tui`
- **Test**: `go test ./...`
- **Generate Mocks**: `go generate ./...` (uses `go.uber.org/mock`)
- **Docker**: A `Dockerfile` is provided for containerized deployment.

## Development Conventions

- **Refactoring**: See `REFACTOR_PLAN.md` for ongoing or planned architectural improvements, especially regarding the DHT component.
- **Concurrency**: Services are started within an `errgroup.Group` in `cmd/serve.go`. Context cancellation is used for graceful shutdown.
- **Database**:
    - Schema is defined in `db/schema/migrations/`.
    - Queries are defined in `db/schema/queries/`.
    - Run `sqlc generate` after modifying queries.
- **Testing**:
    - Use `testify` for assertions.
    - Mocking is done via `go.uber.org/mock/gomock`.
    - Functional tests for DHT and Torznab are located in `tests/functional/`.
- **Logging**: Uses a custom logger wrapper in `logger/`.
- **Metrics**: Prometheus metrics are available (default port 9091).
- **Gluetun Integration**: Supports reading external IP and forwarded port from files (useful when running behind a VPN).

## Project Structure

- `api/`: REST API handlers and server.
- `cmd/`: Cobra CLI commands.
- `config/`: Configuration structure and loading logic.
- `db/`: Database connection, migrations, and `sqlc` generated code.
- `dht/`: DHT protocol implementation, routing table, and crawler logic.
- `gluetun/`: Helpers for integrating with Gluetun VPN.
- `indexer/`: Metadata indexing worker.
- `metadata/`: BitTorrent metadata fetching client.
- `pkg/`: Shared packages (e.g., `torznab` types).
- `scrape/`: Tracker scraping logic.
- `service/`: Business logic layer.
- `torznab/`: Torznab-specific API handlers.
- `tui/`: Bubble Tea TUI implementation.
