ALTER TABLE torrents ADD COLUMN kind_layer TEXT;
ALTER TABLE torrents ADD COLUMN title_key TEXT;
ALTER TABLE torrents ADD COLUMN russian_hint INTEGER NOT NULL DEFAULT 0;

UPDATE torrents SET kind_layer = 'jev' WHERE kind IS NOT NULL;

CREATE INDEX torrents_title_key ON torrents (title_key) WHERE title_key IS NOT NULL;
CREATE INDEX torrents_background ON torrents (id) WHERE classified_at IS NULL AND kind IS NULL AND russian_hint = 1;
