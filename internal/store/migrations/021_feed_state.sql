CREATE TABLE feed_state (
    source TEXT NOT NULL,
    key TEXT NOT NULL,
    done_at INTEGER NOT NULL,
    PRIMARY KEY (source, key)
) WITHOUT ROWID;
