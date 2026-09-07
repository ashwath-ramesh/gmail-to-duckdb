-- name: Weekday counts
SELECT dayofweek(internal_date) AS weekday, count(*)::BIGINT AS n
FROM messages
WHERE NOT is_deleted
GROUP BY 1
ORDER BY 1
