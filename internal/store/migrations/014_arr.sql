CREATE TABLE arr_events (
    id       INTEGER PRIMARY KEY,
    app      TEXT    NOT NULL,
    event    TEXT    NOT NULL,
    key      TEXT    NOT NULL,
    infohash BLOB,
    work     INTEGER REFERENCES tmdb_works (id) ON DELETE SET NULL,
    title    TEXT,
    indexer  TEXT,
    size     INTEGER,
    season   INTEGER,
    episode  INTEGER,
    at       INTEGER NOT NULL,
    UNIQUE (app, event, key)
);

CREATE INDEX arr_events_work ON arr_events (work, event, at) WHERE work IS NOT NULL;
CREATE INDEX arr_events_infohash ON arr_events (infohash) WHERE infohash IS NOT NULL;
CREATE INDEX arr_events_latest ON arr_events (app, event, at);
