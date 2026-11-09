CREATE TABLE torznab_listings (
    source TEXT NOT NULL,
    infohash BLOB NOT NULL,
    title TEXT NOT NULL,
    comments TEXT NOT NULL,
    categories TEXT NOT NULL,
    size INTEGER NOT NULL,
    imdb TEXT NOT NULL,
    tmdb_id INTEGER NOT NULL,
    tvdb_id INTEGER NOT NULL,
    published_at INTEGER NOT NULL,
    recorded_at INTEGER NOT NULL,
    listed_at INTEGER NOT NULL,
    PRIMARY KEY (source, infohash)
) WITHOUT ROWID;
CREATE INDEX torznab_listings_infohash ON torznab_listings (infohash);
CREATE INDEX torznab_listings_listed_at ON torznab_listings (listed_at);
