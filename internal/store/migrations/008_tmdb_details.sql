ALTER TABLE tmdb_works ADD COLUMN type TEXT;
ALTER TABLE tmdb_works ADD COLUMN original_title TEXT;
ALTER TABLE tmdb_works ADD COLUMN poster_path TEXT;
ALTER TABLE tmdb_works ADD COLUMN rating REAL;
ALTER TABLE tmdb_works ADD COLUMN votes INTEGER;
ALTER TABLE tmdb_works ADD COLUMN runtime INTEGER;
ALTER TABLE tmdb_works ADD COLUMN status TEXT;
ALTER TABLE tmdb_works ADD COLUMN seasons INTEGER;
ALTER TABLE tmdb_works ADD COLUMN countries TEXT;
ALTER TABLE tmdb_works ADD COLUMN language TEXT;
ALTER TABLE tmdb_works ADD COLUMN details_at INTEGER;

CREATE INDEX tmdb_works_details ON tmdb_works (matched_at) WHERE layer IS NOT NULL AND details_at IS NULL;

CREATE TABLE tmdb_texts (
    title_key TEXT NOT NULL REFERENCES tmdb_works (title_key) ON DELETE CASCADE,
    lang      TEXT NOT NULL,
    title     TEXT,
    overview  TEXT,
    genres    TEXT,
    PRIMARY KEY (title_key, lang)
) WITHOUT ROWID;
