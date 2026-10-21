ALTER TABLE tmdb_works RENAME TO tmdb_works_old;
ALTER TABLE tmdb_texts RENAME TO tmdb_texts_old;
ALTER TABLE tmdb_ids RENAME TO tmdb_ids_old;
DROP INDEX tmdb_works_details;
DROP INDEX tmdb_ids_id;

CREATE TABLE tmdb_works (
    id             INTEGER PRIMARY KEY,
    type           TEXT    NOT NULL,
    tmdb_id        INTEGER NOT NULL,
    original_title TEXT,
    year           INTEGER,
    poster_path    TEXT,
    rating         REAL,
    votes          INTEGER,
    runtime        INTEGER,
    status         TEXT,
    seasons        INTEGER,
    countries      TEXT,
    language       TEXT,
    popularity     REAL,
    peers_week     INTEGER NOT NULL DEFAULT 0,
    details_at     INTEGER,
    created_at     INTEGER NOT NULL,
    UNIQUE (type, tmdb_id)
);

CREATE INDEX tmdb_works_details ON tmdb_works (created_at) WHERE details_at IS NULL;

CREATE TABLE tmdb_texts (
    work        INTEGER NOT NULL REFERENCES tmdb_works (id) ON DELETE CASCADE,
    lang        TEXT    NOT NULL,
    title       TEXT,
    overview    TEXT,
    genres      TEXT,
    poster_path TEXT,
    PRIMARY KEY (work, lang)
) WITHOUT ROWID;

CREATE TABLE tmdb_ids (
    work     INTEGER NOT NULL REFERENCES tmdb_works (id) ON DELETE CASCADE,
    provider TEXT    NOT NULL,
    id       TEXT    NOT NULL,
    PRIMARY KEY (work, provider)
) WITHOUT ROWID;

CREATE INDEX tmdb_ids_id ON tmdb_ids (provider, id);

CREATE VIRTUAL TABLE tmdb_titles_fts USING fts5 (
    titles,
    tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TABLE tmdb_matches (
    title_key  TEXT    PRIMARY KEY,
    work       INTEGER REFERENCES tmdb_works (id) ON DELETE SET NULL,
    layer      TEXT,
    confidence REAL,
    matched_at INTEGER NOT NULL,
    error      TEXT
) WITHOUT ROWID;

CREATE INDEX tmdb_matches_work ON tmdb_matches (work) WHERE work IS NOT NULL;

CREATE TABLE tmdb_searches (
    kind        TEXT    NOT NULL,
    query       TEXT    NOT NULL,
    searched_at INTEGER NOT NULL,
    works       TEXT    NOT NULL,
    PRIMARY KEY (kind, query)
) WITHOUT ROWID;

CREATE TEMP TABLE m010 AS
SELECT w.title_key AS title_key,
       coalesce(w.type, CASE WHEN w.title_key LIKE 's:%' THEN 'tv' ELSE 'movie' END) AS type,
       CAST(i.id AS INTEGER) AS tmdb_id,
       max(coalesce(w.details_at, 0)) AS details
FROM tmdb_works_old w JOIN tmdb_ids_old i ON i.title_key = w.title_key AND i.provider = 'tmdb'
WHERE w.layer IS NOT NULL
GROUP BY 2, 3;

INSERT INTO tmdb_works (type, tmdb_id, original_title, year, poster_path, rating, votes, runtime, status, seasons, countries, language, details_at, created_at)
SELECT m.type, m.tmdb_id, w.original_title, w.year, w.poster_path, w.rating, w.votes, w.runtime, w.status, w.seasons, w.countries, w.language, w.details_at, w.matched_at
FROM m010 m JOIN tmdb_works_old w ON w.title_key = m.title_key;

INSERT INTO tmdb_texts (work, lang, title, overview, genres, poster_path)
SELECT n.id, t.lang, t.title, t.overview, t.genres, t.poster_path
FROM m010 m JOIN tmdb_works n ON n.type = m.type AND n.tmdb_id = m.tmdb_id JOIN tmdb_texts_old t ON t.title_key = m.title_key;

INSERT INTO tmdb_ids (work, provider, id)
SELECT n.id, i.provider, i.id
FROM m010 m JOIN tmdb_works n ON n.type = m.type AND n.tmdb_id = m.tmdb_id JOIN tmdb_ids_old i ON i.title_key = m.title_key
WHERE i.provider != 'tmdb';

INSERT INTO tmdb_titles_fts (rowid, titles)
SELECT w.id, concat_ws(char(10), w.original_title, (SELECT group_concat(t.title, char(10)) FROM tmdb_texts t WHERE t.work = w.id))
FROM tmdb_works w;

INSERT INTO tmdb_matches (title_key, work, layer, confidence, matched_at, error)
SELECT w.title_key, n.id,
       CASE WHEN n.id IS NULL THEN NULL ELSE w.layer END,
       CASE WHEN n.id IS NULL THEN NULL ELSE w.confidence END,
       w.matched_at, w.error
FROM tmdb_works_old w
LEFT JOIN tmdb_ids_old i ON i.title_key = w.title_key AND i.provider = 'tmdb'
LEFT JOIN tmdb_works n ON n.tmdb_id = CAST(i.id AS INTEGER)
    AND n.type = coalesce(w.type, CASE WHEN w.title_key LIKE 's:%' THEN 'tv' ELSE 'movie' END);

DROP INDEX torrent_releases_work_key;
ALTER TABLE torrent_releases DROP COLUMN work_key;
ALTER TABLE torrent_releases ADD COLUMN work INTEGER REFERENCES tmdb_works (id) ON DELETE SET NULL;
ALTER TABLE torrent_releases ADD COLUMN episode INTEGER;
ALTER TABLE torrent_releases ADD COLUMN quality TEXT;
CREATE INDEX torrent_releases_work ON torrent_releases (work, id) WHERE work IS NOT NULL;

UPDATE torrent_releases SET work = (SELECT m.work FROM tmdb_matches m WHERE m.title_key = torrent_releases.title_key)
WHERE title_key IS NOT NULL;

ALTER TABLE torrent_audio ADD COLUMN mark TEXT;

DROP TABLE stat_top;
CREATE TABLE stat_top (
    kind        TEXT    NOT NULL,
    window      TEXT    NOT NULL,
    voice       TEXT    NOT NULL,
    rank        INTEGER NOT NULL,
    work        INTEGER NOT NULL,
    peers       INTEGER NOT NULL,
    delta       REAL,
    releases    INTEGER NOT NULL,
    computed_at INTEGER NOT NULL,
    PRIMARY KEY (kind, window, voice, rank)
) WITHOUT ROWID;

CREATE INDEX stat_top_work ON stat_top (work);

DROP TABLE tmdb_texts_old;
DROP TABLE tmdb_ids_old;
DROP TABLE tmdb_works_old;
DROP TABLE m010;
