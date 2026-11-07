UPDATE torrent_releases SET kind_layer = 'classifier' WHERE kind_layer = 'jev';
UPDATE torrent_audio SET layer = 'classifier' WHERE layer = 'jev';
UPDATE tmdb_matches SET layer = 'classifier' WHERE layer = 'jev';
UPDATE torrent_journal SET outcome = 'classifier' WHERE kind IN ('classified', 'matched') AND outcome = 'jev';
UPDATE torrent_journal SET detail = json_set(detail, '$.layer', 'classifier')
    WHERE kind = 'matched' AND outcome = 'earlier' AND json_extract(detail, '$.layer') = 'jev';
