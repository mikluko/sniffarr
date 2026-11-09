ALTER TABLE torrent_releases ADD COLUMN quality_layer TEXT;
ALTER TABLE torrent_releases ADD COLUMN hdr TEXT;
ALTER TABLE torrent_releases ADD COLUMN codec TEXT;
ALTER TABLE probe_tracks ADD COLUMN is_default INTEGER NOT NULL DEFAULT 0;
