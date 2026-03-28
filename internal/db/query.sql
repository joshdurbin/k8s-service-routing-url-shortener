-- name: InsertURL :one
INSERT INTO urls (short_code, long_url)
VALUES (?, ?)
RETURNING id, short_code, long_url, created_at;

-- name: GetURLByShortCode :one
SELECT id, short_code, long_url, created_at
FROM urls
WHERE short_code = ?;

-- Cursor-based pagination: pass last seen id (0 for first page).
-- name: ListURLs :many
SELECT id, short_code, long_url, created_at
FROM urls
WHERE id > ?
ORDER BY id ASC
LIMIT ?;

-- name: UpsertFollowStats :exec
INSERT INTO url_stats (short_code, follow_count, first_follow, last_follow)
VALUES (?, 1, ?, ?)
ON CONFLICT(short_code) DO UPDATE SET
    follow_count = url_stats.follow_count + 1,
    last_follow  = excluded.last_follow,
    first_follow = COALESCE(url_stats.first_follow, excluded.first_follow);

-- name: GetFollowStats :one
SELECT short_code, follow_count, first_follow, last_follow
FROM url_stats
WHERE short_code = ?;

-- name: ListURLsWithStats :many
SELECT
    u.id,
    u.short_code,
    u.long_url,
    u.created_at,
    COALESCE(s.follow_count, 0) AS follow_count,
    s.first_follow,
    s.last_follow
FROM urls u
LEFT JOIN url_stats s ON s.short_code = u.short_code
WHERE u.id > ?
ORDER BY u.id ASC
LIMIT ?;

-- name: DeleteURL :exec
DELETE FROM urls WHERE short_code = ?;

-- name: DeleteURLStats :exec
DELETE FROM url_stats WHERE short_code = ?;

-- name: GetCounter :one
SELECT id, value FROM counter WHERE id = 1;

-- name: SetCounter :exec
INSERT INTO counter (id, value) VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET value = excluded.value;

-- name: CountURLs :one
SELECT COUNT(*) AS total FROM urls;
