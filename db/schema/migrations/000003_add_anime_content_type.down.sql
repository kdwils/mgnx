-- Removing an enum value requires recreating the type.
-- Any rows with content_type = 'anime' are reset to 'unknown'.
ALTER TABLE torrents ALTER COLUMN content_type TYPE text;
UPDATE torrents SET content_type = 'unknown' WHERE content_type = 'anime';
DROP TYPE content_type;
CREATE TYPE content_type AS ENUM ('movie', 'tv', 'unknown');
ALTER TABLE torrents ALTER COLUMN content_type TYPE content_type USING content_type::content_type;
