ALTER TABLE queue ADD COLUMN queued_at INTEGER NOT NULL DEFAULT 0;
UPDATE queue SET queued_at = CAST(unixepoch('subsec') * 1000 AS INTEGER);

CREATE INDEX queue_queued_at ON queue (queued_at) WHERE lease_owner IS NULL;
CREATE INDEX queue_evict ON queue (priority DESC, id) WHERE lease_owner IS NULL;

CREATE TABLE dead (
    infohash BLOB    PRIMARY KEY,
    until    INTEGER NOT NULL
) WITHOUT ROWID;

CREATE INDEX dead_until ON dead (until);
