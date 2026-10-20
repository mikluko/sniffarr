CREATE TABLE IF NOT EXISTS queue (
    id          INTEGER PRIMARY KEY,
    infohash    BLOB    NOT NULL UNIQUE,
    priority    INTEGER NOT NULL,
    attempts    INTEGER NOT NULL DEFAULT 0,
    not_before  INTEGER NOT NULL DEFAULT 0,
    lease_owner INTEGER,
    lease_until INTEGER,
    queued_at   INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS queue_order ON queue (priority, id) WHERE lease_owner IS NULL;
CREATE INDEX IF NOT EXISTS queue_lease_owner ON queue (lease_owner) WHERE lease_owner IS NOT NULL;
CREATE INDEX IF NOT EXISTS queue_lease_until ON queue (lease_until) WHERE lease_until IS NOT NULL;
CREATE INDEX IF NOT EXISTS queue_queued_at ON queue (queued_at) WHERE lease_owner IS NULL;
CREATE INDEX IF NOT EXISTS queue_evict ON queue (priority DESC, id) WHERE lease_owner IS NULL;

CREATE TABLE IF NOT EXISTS dead (
    infohash BLOB    PRIMARY KEY,
    until    INTEGER NOT NULL
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS dead_until ON dead (until);

DELETE FROM schema_migrations WHERE component = 'queue';

ALTER TABLE torrents RENAME TO torrent_releases;
ALTER TABLE torrents_fts RENAME TO torrent_releases_fts;
ALTER TABLE files RENAME TO torrent_files;
ALTER TABLE audio RENAME TO torrent_audio;
ALTER TABLE queue RENAME TO ingest_queue;
ALTER TABLE dead RENAME TO ingest_dead;
ALTER TABLE rejected RENAME TO ingest_rejected;
ALTER TABLE token_usage RENAME TO ingest_token_usage;
ALTER TABLE peers_hour RENAME TO stat_peers_by_hour;
ALTER TABLE peers_day RENAME TO stat_peers_by_day;
ALTER TABLE top RENAME TO stat_top;

DROP INDEX torrents_unclassified;
DROP INDEX torrents_last_seen;
DROP INDEX torrents_title_key;
DROP INDEX torrents_background;
DROP INDEX torrents_titled;
DROP INDEX torrents_work_key;
DROP INDEX torrents_peers_week;
DROP INDEX torrents_size;
CREATE INDEX torrent_releases_unclassified ON torrent_releases (id) WHERE classified_at IS NULL;
CREATE INDEX torrent_releases_last_seen ON torrent_releases (last_seen);
CREATE INDEX torrent_releases_title_key ON torrent_releases (title_key) WHERE title_key IS NOT NULL;
CREATE INDEX torrent_releases_background ON torrent_releases (id) WHERE classified_at IS NULL AND kind IS NULL;
CREATE INDEX torrent_releases_titled ON torrent_releases (title_key, id) WHERE title_key IS NOT NULL AND kind IN ('movie', 'series');
CREATE INDEX torrent_releases_work_key ON torrent_releases (work_key) WHERE work_key IS NOT NULL;
CREATE INDEX torrent_releases_peers_week ON torrent_releases (peers_week, id);
CREATE INDEX torrent_releases_size ON torrent_releases (size, id);

DROP INDEX queue_order;
DROP INDEX queue_lease_owner;
DROP INDEX queue_lease_until;
DROP INDEX queue_queued_at;
DROP INDEX queue_evict;
CREATE INDEX ingest_queue_order ON ingest_queue (priority, id) WHERE lease_owner IS NULL;
CREATE INDEX ingest_queue_lease_owner ON ingest_queue (lease_owner) WHERE lease_owner IS NOT NULL;
CREATE INDEX ingest_queue_lease_until ON ingest_queue (lease_until) WHERE lease_until IS NOT NULL;
CREATE INDEX ingest_queue_queued_at ON ingest_queue (queued_at) WHERE lease_owner IS NULL;
CREATE INDEX ingest_queue_evict ON ingest_queue (priority DESC, id) WHERE lease_owner IS NULL;

DROP INDEX dead_until;
CREATE INDEX ingest_dead_until ON ingest_dead (until);

DROP INDEX peers_hour_hour;
CREATE INDEX stat_peers_by_hour_hour ON stat_peers_by_hour (hour);

DROP INDEX peers_day_day;
CREATE INDEX stat_peers_by_day_day ON stat_peers_by_day (day);

DROP INDEX top_work;
CREATE INDEX stat_top_work ON stat_top (work_key);

DROP INDEX title_ids_id;
CREATE INDEX tmdb_ids_id ON tmdb_ids (provider, id);
