CREATE TABLE ingest_dead_keyed (
    key   INTEGER PRIMARY KEY,
    until INTEGER NOT NULL
);

INSERT INTO ingest_dead_keyed (key, until)
SELECT ingest_dead_key(infohash), max(until) FROM ingest_dead GROUP BY 1;

DROP TABLE ingest_dead;
ALTER TABLE ingest_dead_keyed RENAME TO ingest_dead;
CREATE INDEX ingest_dead_until ON ingest_dead (until);
