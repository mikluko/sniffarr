CREATE TABLE IF NOT EXISTS task_backlog (
    id         INTEGER PRIMARY KEY,
    kind       TEXT    NOT NULL,
    infohash   BLOB    NOT NULL,
    priority   INTEGER NOT NULL,
    attempts   INTEGER NOT NULL,
    not_before INTEGER NOT NULL
);

INSERT INTO task_backlog (kind, infohash, priority, attempts, not_before)
    SELECT kind, infohash, priority, attempts, not_before FROM task_queue ORDER BY priority, id;

DROP TABLE task_queue;
