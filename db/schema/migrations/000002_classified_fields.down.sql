ALTER TABLE torrents
    DROP COLUMN IF EXISTS classified_title,
    DROP COLUMN IF EXISTS classified_year,
    DROP COLUMN IF EXISTS classified_season,
    DROP COLUMN IF EXISTS classified_episode;
