-- name: Largest threads
SELECT thread_id, count(*)::BIGINT AS n, min(subject) AS subject
FROM messages
WHERE NOT is_deleted
GROUP BY 1
ORDER BY n DESC
LIMIT 25
