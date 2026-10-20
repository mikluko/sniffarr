CREATE TABLE audio (
    torrent_id  INTEGER NOT NULL REFERENCES torrents (id) ON DELETE CASCADE,
    language    TEXT    NOT NULL,
    marker      INTEGER NOT NULL,
    script_hint INTEGER NOT NULL,
    probability REAL,
    layer       TEXT,
    PRIMARY KEY (torrent_id, language)
) WITHOUT ROWID;

INSERT INTO audio (torrent_id, language, marker, script_hint, probability, layer)
SELECT id, 'ru', russian_marker, russian_hint,
       CASE WHEN russian_marker THEN 1.0 ELSE russian_audio END,
       CASE WHEN russian_marker THEN 'rules' WHEN russian_audio IS NOT NULL THEN 'jev' END
FROM torrents;

DROP INDEX torrents_background;
ALTER TABLE torrents DROP COLUMN russian_marker;
ALTER TABLE torrents DROP COLUMN russian_hint;
ALTER TABLE torrents DROP COLUMN russian_audio;

CREATE INDEX torrents_background ON torrents (id) WHERE classified_at IS NULL AND kind IS NULL;
