CREATE TABLE stat_top_history (
    kind       TEXT    NOT NULL,
    window     TEXT    NOT NULL,
    voice      TEXT    NOT NULL,
    work       INTEGER NOT NULL,
    first_at   INTEGER NOT NULL,
    entered_at INTEGER NOT NULL,
    last_at    INTEGER NOT NULL,
    prior      INTEGER NOT NULL,
    entries    INTEGER NOT NULL,
    peak       INTEGER NOT NULL,
    PRIMARY KEY (kind, window, voice, work)
) WITHOUT ROWID;

CREATE INDEX stat_top_history_work ON stat_top_history (work);
