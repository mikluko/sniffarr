CREATE TABLE crawl_state (
    source TEXT NOT NULL,
    url TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    status INTEGER NOT NULL,
    etag TEXT NOT NULL,
    last_modified TEXT NOT NULL,
    error TEXT NOT NULL,
    PRIMARY KEY (source, url)
) WITHOUT ROWID;
