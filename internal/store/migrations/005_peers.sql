CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value BLOB NOT NULL
) WITHOUT ROWID;

ALTER TABLE torrents ADD COLUMN work_key TEXT;
ALTER TABLE torrents ADD COLUMN year INTEGER;
ALTER TABLE torrents ADD COLUMN season INTEGER;
ALTER TABLE torrents ADD COLUMN peers_week INTEGER NOT NULL DEFAULT 0;

CREATE INDEX torrents_work_key ON torrents (work_key) WHERE work_key IS NOT NULL;
CREATE INDEX torrents_peers_week ON torrents (peers_week, id);
CREATE INDEX torrents_size ON torrents (size, id);

CREATE TABLE peers_hour (
    torrent_id INTEGER NOT NULL REFERENCES torrents (id) ON DELETE CASCADE,
    hour       INTEGER NOT NULL,
    sketch     BLOB    NOT NULL,
    PRIMARY KEY (torrent_id, hour)
) WITHOUT ROWID;

CREATE INDEX peers_hour_hour ON peers_hour (hour);

CREATE TABLE peers_day (
    torrent_id INTEGER NOT NULL REFERENCES torrents (id) ON DELETE CASCADE,
    day        INTEGER NOT NULL,
    sketch     BLOB    NOT NULL,
    PRIMARY KEY (torrent_id, day)
) WITHOUT ROWID;

CREATE INDEX peers_day_day ON peers_day (day);

CREATE TABLE top (
    kind        TEXT    NOT NULL,
    window      TEXT    NOT NULL,
    voice       TEXT    NOT NULL,
    rank        INTEGER NOT NULL,
    work_key    TEXT    NOT NULL,
    peers       INTEGER NOT NULL,
    delta       REAL,
    releases    INTEGER NOT NULL,
    computed_at INTEGER NOT NULL,
    PRIMARY KEY (kind, window, voice, rank)
) WITHOUT ROWID;

CREATE INDEX top_work ON top (work_key);
