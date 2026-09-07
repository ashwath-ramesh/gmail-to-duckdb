-- name: Label counts
SELECT COALESCE(l.name, lbl) AS label, count(*)::BIGINT AS n
FROM messages m, UNNEST(m.label_ids) AS t(lbl)
LEFT JOIN labels l ON l.id = lbl
WHERE NOT m.is_deleted
GROUP BY 1
ORDER BY n DESC
