-- name: Unread by sender
SELECT from_email, count(*)::BIGINT AS n
FROM messages
WHERE NOT is_deleted AND NOT is_read
GROUP BY 1
ORDER BY n DESC
LIMIT 25
