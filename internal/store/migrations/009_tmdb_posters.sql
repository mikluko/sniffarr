ALTER TABLE tmdb_texts ADD COLUMN poster_path TEXT;

UPDATE tmdb_works SET details_at = NULL WHERE layer IS NOT NULL AND details_at IS NOT NULL;
