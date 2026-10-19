CREATE TABLE torrents (
    id                INTEGER PRIMARY KEY,
    infohash          BLOB    NOT NULL UNIQUE,
    name              TEXT    NOT NULL,
    size              INTEGER NOT NULL,
    file_count        INTEGER NOT NULL,
    discovery         TEXT    NOT NULL,
    first_seen        INTEGER NOT NULL,
    last_seen         INTEGER NOT NULL,
    russian_marker    INTEGER NOT NULL,
    classified_at     INTEGER,
    classify_error    TEXT,
    kind              TEXT,
    kind_confidence   REAL,
    source            TEXT,
    source_confidence REAL,
    russian_audio     REAL
);

CREATE INDEX torrents_unclassified ON torrents (id) WHERE classified_at IS NULL;
CREATE INDEX torrents_last_seen ON torrents (last_seen);

CREATE TABLE files (
    torrent_id INTEGER NOT NULL REFERENCES torrents (id) ON DELETE CASCADE,
    idx        INTEGER NOT NULL,
    path       TEXT    NOT NULL,
    size       INTEGER NOT NULL,
    PRIMARY KEY (torrent_id, idx)
) WITHOUT ROWID;

CREATE TABLE rejected (
    infohash    BLOB    PRIMARY KEY,
    reason      TEXT    NOT NULL,
    rejected_at INTEGER NOT NULL
) WITHOUT ROWID;

CREATE VIRTUAL TABLE torrents_fts USING fts5 (
    name,
    paths,
    content = '',
    contentless_delete = 1,
    tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TABLE token_usage (
    day    TEXT    PRIMARY KEY,
    tokens INTEGER NOT NULL
) WITHOUT ROWID;
