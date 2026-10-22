CREATE INDEX torrent_releases_counts ON torrent_releases (source, kind_layer, kind);
CREATE INDEX torrent_releases_first_seen ON torrent_releases (first_seen);
CREATE INDEX ingest_rejected_reason ON ingest_rejected (reason);
