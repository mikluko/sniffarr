CREATE TABLE feed_items (
    source TEXT NOT NULL,
    infohash BLOB NOT NULL,
    title TEXT NOT NULL,
    url TEXT NOT NULL,
    category TEXT NOT NULL,
    size INTEGER NOT NULL,
    published_at INTEGER NOT NULL,
    recorded_at INTEGER NOT NULL,
    PRIMARY KEY (source, infohash)
) WITHOUT ROWID;
CREATE INDEX feed_items_infohash ON feed_items (infohash);
UPDATE ingest_queue SET priority = -2 WHERE priority = -1;
