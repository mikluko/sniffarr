CREATE TABLE queue (
    id          INTEGER PRIMARY KEY,
    infohash    BLOB    NOT NULL UNIQUE,
    priority    INTEGER NOT NULL,
    attempts    INTEGER NOT NULL DEFAULT 0,
    not_before  INTEGER NOT NULL DEFAULT 0,
    lease_owner INTEGER,
    lease_until INTEGER
);

CREATE INDEX queue_order ON queue (priority, id) WHERE lease_owner IS NULL;
CREATE INDEX queue_lease_owner ON queue (lease_owner) WHERE lease_owner IS NOT NULL;
CREATE INDEX queue_lease_until ON queue (lease_until) WHERE lease_until IS NOT NULL;
