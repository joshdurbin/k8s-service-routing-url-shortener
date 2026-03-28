-- +goose Up
CREATE TABLE IF NOT EXISTS urls (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    short_code TEXT    NOT NULL UNIQUE,
    long_url   TEXT    NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS url_stats (
    short_code   TEXT    PRIMARY KEY REFERENCES urls(short_code),
    follow_count INTEGER NOT NULL DEFAULT 0,
    first_follow INTEGER,
    last_follow  INTEGER
);

-- Single-row counter table. The CHECK constraint enforces only one row.
CREATE TABLE IF NOT EXISTS counter (
    id    INTEGER PRIMARY KEY CHECK (id = 1),
    value INTEGER NOT NULL DEFAULT 0
);

INSERT OR IGNORE INTO counter (id, value) VALUES (1, 0);

-- +goose Down
DROP TABLE IF EXISTS url_stats;
DROP TABLE IF EXISTS urls;
DROP TABLE IF EXISTS counter;
