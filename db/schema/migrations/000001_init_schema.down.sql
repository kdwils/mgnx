-- Indexes
DROP INDEX IF EXISTS idx_tmdb_tv_genres_genre_id;
DROP INDEX IF EXISTS idx_tmdb_tv_imdb_id;
DROP INDEX IF EXISTS idx_tmdb_tv_series_lookup;
DROP INDEX IF EXISTS idx_tmdb_movie_genres_genre_id;
DROP INDEX IF EXISTS idx_tmdb_movies_imdb_id;
DROP INDEX IF EXISTS idx_scrape_history_scraped_at;
DROP INDEX IF EXISTS idx_scrape_history_tracker_id;
DROP INDEX IF EXISTS idx_scrape_history_infohash;
DROP INDEX IF EXISTS idx_torrent_trackers_tracker_id;
DROP INDEX IF EXISTS idx_torrent_files_infohash_video;
DROP INDEX IF EXISTS idx_torrent_files_infohash;
DROP INDEX IF EXISTS idx_torrents_name_fts;
DROP INDEX IF EXISTS idx_torrents_content_state_seeders;
DROP INDEX IF EXISTS idx_torrents_dead_candidates;
DROP INDEX IF EXISTS idx_torrents_scrape_at;
DROP INDEX IF EXISTS idx_torrents_classified;
DROP INDEX IF EXISTS idx_torrents_pending;

-- Tables (reverse dependency order)
DROP TABLE IF EXISTS tmdb_tv_genres;
DROP TABLE IF EXISTS tmdb_movie_genres;
DROP TABLE IF EXISTS tmdb_tv;
DROP TABLE IF EXISTS tmdb_movies;
DROP TABLE IF EXISTS genres;
DROP TABLE IF EXISTS scrape_history;
DROP TABLE IF EXISTS torrent_trackers;
DROP TABLE IF EXISTS trackers;
DROP TABLE IF EXISTS torrent_files;
DROP TABLE IF EXISTS torrents;

-- Types
DROP TYPE IF EXISTS content_type;
DROP TYPE IF EXISTS torrent_state;
