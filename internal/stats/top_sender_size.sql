-- name: Top senders by size
SELECT from_email, sum(size_bytes)::BIGINT AS bytes
FROM messages
WHERE NOT is_deleted AND NOT is_outgoing
GROUP BY 1
ORDER BY bytes DESC
LIMIT 25
