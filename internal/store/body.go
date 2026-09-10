package store

import (
	"context"
	"time"
)

type BodyUpdate struct {
	ID      string
	Body    string
	Headers []Header
}

func (d *DB) UpdateBody(ctx context.Context, id, body string) error {
	return d.ApplyBodyUpdates(ctx, []BodyUpdate{{ID: id, Body: body}}, nil)
}

func (d *DB) ApplyBodyUpdates(ctx context.Context, updates []BodyUpdate, tombs []string) error {
	if len(updates) == 0 && len(tombs) == 0 {
		return nil
	}
	if err := d.acquire(ctx); err != nil {
		return err
	}
	defer d.release()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := markFTSDirty(ctx, tx); err != nil {
		return err
	}
	ids := make([]string, 0, len(updates))
	now := time.Now().UTC()
	for _, u := range updates {
		if u.ID == "" {
			continue
		}
		var headers any
		if u.Headers != nil {
			headers = encodeHeaders(u.Headers)
		}
		res, err := tx.ExecContext(ctx, `
UPDATE messages SET
  body = ?,
  has_body = (? <> ''),
  body_fetched = true,
  headers = CASE WHEN headers IS NULL THEN CAST(? AS JSON) ELSE headers END,
  synced_at = ?
WHERE id = ? AND NOT COALESCE(body_fetched, false)
`, u.Body, u.Body, headers, now, u.ID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			ids = append(ids, u.ID)
		}
	}
	if err := markDeletedTx(ctx, tx, tombs); err != nil {
		return err
	}
	if err := refreshSearchTextTx(ctx, tx, ids); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) IDsNeedingFetch(ctx context.Context, afterID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 500
	}
	q := `
SELECT id FROM messages
WHERE NOT COALESCE(body_fetched, false) AND NOT is_deleted`
	args := make([]any, 0, 2)
	if afterID != "" {
		q += ` AND id > ?`
		args = append(args, afterID)
	}
	q += ` ORDER BY id LIMIT ?`
	args = append(args, limit)
	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
