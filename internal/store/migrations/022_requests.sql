ALTER TABLE probe_releases ADD COLUMN requested_at INTEGER;

CREATE TABLE peer_lookups (
    infohash  BLOB    PRIMARY KEY,
    wanted_at INTEGER NOT NULL
) WITHOUT ROWID;
