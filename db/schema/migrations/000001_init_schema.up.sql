-- ---------------------------------------------------------------------------
-- Enum types
-- ---------------------------------------------------------------------------

CREATE TYPE torrent_state AS ENUM (
    'pending',
    'classified',
    'enriched',
    'active',
    'dead',
    'rejected'
);

CREATE TYPE content_type AS ENUM (
    'movie',
    'tv',
    'unknown'
);

-- ---------------------------------------------------------------------------
-- torrents
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS torrents (
    infohash                        TEXT PRIMARY KEY,
    name                            TEXT NOT NULL CHECK (char_length(name) <= 500),
    total_size                      BIGINT NOT NULL CHECK (total_size >= 0),
    file_count                      BIGINT NOT NULL DEFAULT 0 CHECK (file_count >= 0),
    state                           torrent_state NOT NULL DEFAULT 'pending',
    content_type                    content_type NOT NULL DEFAULT 'unknown',
    quality                         TEXT,
    encoding                        TEXT,
    dynamic_range                   TEXT,
    source                          TEXT,
    release_group                   TEXT,
    scene_name                      TEXT,
    seeders                         BIGINT NOT NULL DEFAULT 0 CHECK (seeders >= 0),
    leechers                        BIGINT NOT NULL DEFAULT 0 CHECK (leechers >= 0),
    scrape_at                       TIMESTAMPTZ,
    enrichment_attempts             INT NOT NULL DEFAULT 0 CHECK (enrichment_attempts >= 0),
    enrichment_last_attempted_at    TIMESTAMPTZ,
    first_seen                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen                       TIMESTAMPTZ,
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_torrents_pending
    ON torrents (first_seen ASC)
    WHERE state = 'pending';

CREATE INDEX IF NOT EXISTS idx_torrents_classified
    ON torrents (first_seen ASC)
    WHERE state = 'classified';

CREATE INDEX IF NOT EXISTS idx_torrents_scrape_at
    ON torrents (scrape_at ASC)
    WHERE state IN ('classified', 'enriched', 'active');

CREATE INDEX IF NOT EXISTS idx_torrents_dead_candidates
    ON torrents (last_seen ASC)
    WHERE state != 'dead' AND state != 'rejected' AND seeders = 0;

CREATE INDEX IF NOT EXISTS idx_torrents_content_state_seeders
    ON torrents (content_type, state, seeders DESC);

CREATE INDEX IF NOT EXISTS idx_torrents_name_fts
    ON torrents USING GIN (to_tsvector('english', name));

-- ---------------------------------------------------------------------------
-- torrent_files
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS torrent_files (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    infohash    TEXT NOT NULL REFERENCES torrents (infohash) ON DELETE CASCADE,
    path        TEXT NOT NULL,
    size        BIGINT NOT NULL CHECK (size >= 0),
    extension   TEXT,
    is_video    BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_torrent_files_infohash
    ON torrent_files (infohash);

CREATE INDEX IF NOT EXISTS idx_torrent_files_infohash_video
    ON torrent_files (infohash)
    WHERE is_video = true;

-- ---------------------------------------------------------------------------
-- trackers + torrent_trackers
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS trackers (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    url         TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS torrent_trackers (
    infohash    TEXT    NOT NULL REFERENCES torrents (infohash) ON DELETE CASCADE,
    tracker_id  BIGINT  NOT NULL REFERENCES trackers (id)      ON DELETE CASCADE,
    last_scrape TIMESTAMPTZ,
    is_active   BOOLEAN NOT NULL DEFAULT true,
    PRIMARY KEY (infohash, tracker_id)
);

CREATE INDEX IF NOT EXISTS idx_torrent_trackers_tracker_id
    ON torrent_trackers (tracker_id);

-- ---------------------------------------------------------------------------
-- scrape_history
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS scrape_history (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    infohash    TEXT    NOT NULL REFERENCES torrents (infohash) ON DELETE CASCADE,
    tracker_id  BIGINT  NOT NULL REFERENCES trackers (id)      ON DELETE CASCADE,
    scraped_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    seeders     BIGINT NOT NULL CHECK (seeders >= 0),
    leechers    BIGINT NOT NULL CHECK (leechers >= 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_scrape_history_infohash
    ON scrape_history (infohash);

CREATE INDEX IF NOT EXISTS idx_scrape_history_tracker_id
    ON scrape_history (tracker_id);

CREATE INDEX IF NOT EXISTS idx_scrape_history_scraped_at
    ON scrape_history (scraped_at ASC);

-- ---------------------------------------------------------------------------
-- genres
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS genres (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tmdb_id     INT NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('movie', 'tv')),
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tmdb_id, kind)
);

-- ---------------------------------------------------------------------------
-- tmdb_movies
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS tmdb_movies (
    infohash         TEXT PRIMARY KEY REFERENCES torrents (infohash) ON DELETE CASCADE,
    tmdb_id          BIGINT NOT NULL,
    imdb_id          TEXT,
    title            TEXT NOT NULL,
    original_title   TEXT,
    year             INT,
    overview         TEXT,
    runtime          INT CHECK (runtime > 0),
    language         TEXT,
    popularity       DOUBLE PRECISION,
    vote_average     DOUBLE PRECISION,
    poster_path      TEXT,
    backdrop_path    TEXT,
    match_confidence DOUBLE PRECISION NOT NULL CHECK (match_confidence BETWEEN 0 AND 1),
    matched_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_tmdb_movies_imdb_id
    ON tmdb_movies (imdb_id)
    WHERE imdb_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- tmdb_movie_genres
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS tmdb_movie_genres (
    infohash    TEXT    NOT NULL REFERENCES tmdb_movies (infohash) ON DELETE CASCADE,
    genre_id    BIGINT  NOT NULL REFERENCES genres      (id)       ON DELETE CASCADE,
    PRIMARY KEY (infohash, genre_id)
);

CREATE INDEX IF NOT EXISTS idx_tmdb_movie_genres_genre_id
    ON tmdb_movie_genres (genre_id);

-- ---------------------------------------------------------------------------
-- tmdb_tv
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS tmdb_tv (
    infohash         TEXT PRIMARY KEY REFERENCES torrents (infohash) ON DELETE CASCADE,
    tmdb_id          BIGINT NOT NULL,
    imdb_id          TEXT,
    series_name      TEXT NOT NULL,
    season           INT CHECK (season > 0),
    episode          INT CHECK (episode > 0),
    episode_name     TEXT,
    overview         TEXT,
    first_air_date   DATE,
    match_confidence DOUBLE PRECISION NOT NULL CHECK (match_confidence BETWEEN 0 AND 1),
    matched_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_tmdb_tv_series_lookup
    ON tmdb_tv (tmdb_id, season, episode);

CREATE INDEX IF NOT EXISTS idx_tmdb_tv_imdb_id
    ON tmdb_tv (imdb_id)
    WHERE imdb_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- tmdb_tv_genres
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS tmdb_tv_genres (
    infohash    TEXT    NOT NULL REFERENCES tmdb_tv (infohash) ON DELETE CASCADE,
    genre_id    BIGINT  NOT NULL REFERENCES genres  (id)       ON DELETE CASCADE,
    PRIMARY KEY (infohash, genre_id)
);

CREATE INDEX IF NOT EXISTS idx_tmdb_tv_genres_genre_id
    ON tmdb_tv_genres (genre_id);
