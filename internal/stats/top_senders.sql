-- name: Top senders
SELECT from_email, count(*)::BIGINT AS n
FROM messages
WHERE NOT is_deleted
GROUP BY 1
ORDER BY n DESC
LIMIT 25
