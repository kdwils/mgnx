ALTER TABLE torrents
    ADD COLUMN IF NOT EXISTS classified_title   TEXT,
    ADD COLUMN IF NOT EXISTS classified_year    INT,
    ADD COLUMN IF NOT EXISTS classified_season  INT,
    ADD COLUMN IF NOT EXISTS classified_episode INT;
