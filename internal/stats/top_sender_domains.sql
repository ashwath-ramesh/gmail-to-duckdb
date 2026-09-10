-- name: Top incoming sender domains
SELECT domain, count(*)::BIGINT AS n, sum(size_bytes)::BIGINT AS bytes
FROM (
  SELECT
    nullif(regexp_extract(lower(from_email), '^[^[:space:]@]+@([^[:space:]@]+)$', 1), '') AS domain,
    size_bytes
  FROM messages
  WHERE NOT is_deleted AND NOT is_outgoing
)
WHERE domain IS NOT NULL
GROUP BY domain
ORDER BY n DESC, domain
LIMIT 25
