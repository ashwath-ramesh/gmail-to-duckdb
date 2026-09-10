-- name: Top outgoing recipient domains
SELECT domain, count(*)::BIGINT AS n, sum(bytes)::BIGINT AS bytes
FROM (
  SELECT
    m.id,
    nullif(regexp_extract(lower(addr), '^[^[:space:]@]+@([^[:space:]@]+)$', 1), '') AS domain,
    any_value(m.size_bytes) AS bytes
  FROM messages m,
       UNNEST(list_concat(COALESCE(m.to_emails, []::VARCHAR[]), COALESCE(m.cc_emails, []::VARCHAR[]))) AS t(addr)
  WHERE NOT m.is_deleted AND m.is_outgoing
  GROUP BY ALL
)
WHERE domain IS NOT NULL
GROUP BY domain
ORDER BY n DESC, domain
LIMIT 25
