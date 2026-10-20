CREATE TABLE titles (
    title_key  TEXT    PRIMARY KEY,
    year       INTEGER,
    confidence REAL,
    layer      TEXT,
    matched_at INTEGER NOT NULL,
    error      TEXT
) WITHOUT ROWID;

CREATE TABLE title_ids (
    title_key TEXT NOT NULL REFERENCES titles (title_key) ON DELETE CASCADE,
    provider  TEXT NOT NULL,
    id        TEXT NOT NULL,
    PRIMARY KEY (title_key, provider)
) WITHOUT ROWID;

CREATE INDEX title_ids_id ON title_ids (provider, id);
CREATE INDEX torrents_titled ON torrents (title_key, id) WHERE title_key IS NOT NULL AND kind IN ('movie', 'series');
