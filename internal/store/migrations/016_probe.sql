CREATE TABLE probe_releases (
    torrent_id INTEGER PRIMARY KEY REFERENCES torrent_releases (id) ON DELETE CASCADE,
    wanted_at  INTEGER,
    outcome    TEXT,
    attempts   INTEGER NOT NULL DEFAULT 0,
    retry_at   INTEGER,
    probed_at  INTEGER,
    path       TEXT,
    format     TEXT,
    downloaded INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX probe_releases_wanted ON probe_releases (wanted_at) WHERE wanted_at IS NOT NULL;

CREATE TABLE probe_tracks (
    torrent_id     INTEGER NOT NULL REFERENCES torrent_releases (id) ON DELETE CASCADE,
    idx            INTEGER NOT NULL,
    kind           TEXT    NOT NULL,
    language       TEXT    NOT NULL,
    language_bcp47 TEXT    NOT NULL,
    name           TEXT    NOT NULL,
    codec          TEXT    NOT NULL,
    channels       INTEGER NOT NULL,
    width          INTEGER NOT NULL,
    height         INTEGER NOT NULL,
    hdr            TEXT    NOT NULL,
    PRIMARY KEY (torrent_id, idx)
) WITHOUT ROWID;

CREATE TABLE probe_usage (
    day   TEXT    PRIMARY KEY,
    bytes INTEGER NOT NULL
) WITHOUT ROWID;
