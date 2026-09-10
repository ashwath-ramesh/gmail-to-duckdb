-- name: Monthly incoming top senders
SELECT month, from_email, count
FROM (
  WITH bounds AS (
    SELECT date_trunc('month', CAST(timezone('UTC', current_timestamp) AS TIMESTAMP)) AS cur
  ),
  windowed AS (
    SELECT m.from_email, m.internal_date
    FROM messages m, bounds b
    WHERE NOT m.is_deleted
      AND NOT m.is_outgoing
      AND trim(COALESCE(m.from_email, '')) <> ''
      AND m.internal_date >= b.cur - INTERVAL 11 MONTH
      AND m.internal_date < b.cur + INTERVAL 1 MONTH
  ),
  top AS (
    SELECT from_email
    FROM windowed
    GROUP BY from_email
    ORDER BY count(*) DESC, from_email
    LIMIT 25
  )
  SELECT date_trunc('month', w.internal_date)::DATE AS month,
         w.from_email,
         count(*)::BIGINT AS count
  FROM windowed w
  INNER JOIN top t ON t.from_email = w.from_email
  GROUP BY 1, 2
)
ORDER BY month DESC, from_email
