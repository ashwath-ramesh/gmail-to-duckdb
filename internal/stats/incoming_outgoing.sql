-- name: Incoming vs outgoing
SELECT CASE WHEN is_outgoing THEN 'outgoing' ELSE 'incoming' END AS direction,
       count(*)::BIGINT AS n
FROM messages
WHERE NOT is_deleted
GROUP BY 1
ORDER BY 1
