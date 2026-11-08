ALTER TABLE torrent_releases ADD COLUMN reading_prefix_end INTEGER;
ALTER TABLE torrent_releases ADD COLUMN reading_title_pos INTEGER;
ALTER TABLE torrent_releases ADD COLUMN reading_title_end INTEGER;
ALTER TABLE torrent_releases ADD COLUMN reading_title TEXT;
ALTER TABLE torrent_releases ADD COLUMN reading_title_confidence REAL;
