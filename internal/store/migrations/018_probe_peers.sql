ALTER TABLE probe_releases ADD COLUMN peers_week INTEGER;

UPDATE probe_releases SET retry_at = probed_at, peers_week = (SELECT t.peers_week FROM torrent_releases t WHERE t.id = torrent_id)
    WHERE outcome = 'failed' AND retry_at IS NULL;
