CREATE TABLE urls (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    short_code TEXT    NOT NULL UNIQUE,
    long_url   TEXT    NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE url_stats (
    short_code   TEXT    PRIMARY KEY REFERENCES urls(short_code),
    follow_count INTEGER NOT NULL DEFAULT 0,
    first_follow INTEGER,
    last_follow  INTEGER
);

CREATE TABLE counter (
    id    INTEGER PRIMARY KEY CHECK (id = 1),
    value INTEGER NOT NULL DEFAULT 0
);
