CREATE TABLE torrent_journal (
    id       INTEGER PRIMARY KEY,
    infohash BLOB    NOT NULL,
    at       INTEGER NOT NULL,
    kind     TEXT    NOT NULL,
    outcome  TEXT    NOT NULL,
    detail   TEXT
);

CREATE INDEX torrent_journal_infohash ON torrent_journal (infohash, id);
CREATE INDEX torrent_journal_at ON torrent_journal (at);

ALTER TABLE task_queue ADD COLUMN leases INTEGER NOT NULL DEFAULT 0;
