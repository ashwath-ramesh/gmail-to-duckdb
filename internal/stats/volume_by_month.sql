-- name: Volume by month
SELECT date_trunc('month', internal_date)::DATE AS month, count(*)::BIGINT AS n
FROM messages
WHERE NOT is_deleted
GROUP BY 1
ORDER BY 1 DESC
