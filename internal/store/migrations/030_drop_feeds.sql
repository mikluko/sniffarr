DROP TABLE feed_items;
DROP TABLE crawl_state;
DELETE FROM torrent_journal WHERE kind = 'feed';
UPDATE task_queue SET priority = priority - 1 WHERE priority >= 3;
