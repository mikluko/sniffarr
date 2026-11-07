CREATE TABLE task_queue (
    id          INTEGER PRIMARY KEY,
    kind        TEXT    NOT NULL,
    infohash    BLOB    NOT NULL,
    priority    INTEGER NOT NULL,
    queued_at   INTEGER NOT NULL,
    attempts    INTEGER NOT NULL DEFAULT 0,
    not_before  INTEGER NOT NULL DEFAULT 0,
    lease_owner INTEGER,
    lease_until INTEGER,
    UNIQUE (kind, infohash)
);

INSERT INTO task_queue (id, kind, infohash, priority, queued_at, attempts, not_before, lease_owner, lease_until)
    SELECT id, 'fetch', infohash, priority + 3, queued_at, attempts, not_before, lease_owner, lease_until FROM ingest_queue;

INSERT INTO task_queue (kind, infohash, priority, queued_at, not_before)
    SELECT 'probe', t.infohash, 1, p.wanted_at * 1000, coalesce(p.retry_at, 0) * 1000
    FROM probe_releases p JOIN torrent_releases t ON t.id = p.torrent_id
    WHERE p.wanted_at IS NOT NULL AND (p.outcome IS NULL OR p.outcome = 'failed' AND p.peers_week IS NULL);

DROP TABLE ingest_queue;

CREATE INDEX task_queue_order ON task_queue (priority, id) WHERE lease_owner IS NULL;
CREATE INDEX task_queue_lease_owner ON task_queue (lease_owner) WHERE lease_owner IS NOT NULL;
CREATE INDEX task_queue_lease_until ON task_queue (lease_until) WHERE lease_until IS NOT NULL;
CREATE INDEX task_queue_queued_at ON task_queue (queued_at) WHERE lease_owner IS NULL;
CREATE INDEX task_queue_evict ON task_queue (priority DESC, id) WHERE lease_owner IS NULL;

ALTER TABLE ingest_dead RENAME TO task_dead;
DROP INDEX ingest_dead_until;
CREATE INDEX task_dead_until ON task_dead (until);

DROP INDEX probe_releases_wanted;
ALTER TABLE probe_releases DROP COLUMN wanted_at;
