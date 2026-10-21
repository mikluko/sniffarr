CREATE INDEX torrent_releases_idle ON torrent_releases (last_seen) WHERE work IS NULL;
