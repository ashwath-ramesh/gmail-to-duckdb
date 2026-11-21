package store

import (
	"context"
	"database/sql"
)

// Wrappers that only tests call. Production writes go through CommitSyncPage,
// ApplyBodyUpdates, and the other transaction paths that these wrap.

func (d *DB) ResetSeen(ctx context.Context) error {
	return d.withTx(ctx, func(tx *sql.Tx) error {
		return resetSeenTx(ctx, tx)
	})
}

func (d *DB) AddSeen(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return d.withTx(ctx, func(tx *sql.Tx) error {
		return addSeenTx(ctx, tx, ids)
	})
}

func (d *DB) MarkMissingDeleted(ctx context.Context) (int64, error) {
	var n int64
	err := d.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = markMissingDeletedTx(ctx, tx)
		return err
	})
	if err == nil && n > 0 {
		d.notifyIndex()
	}
	return n, err
}

func (d *DB) MarkDeleted(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	err := d.withTx(ctx, func(tx *sql.Tx) error {
		return markDeletedTx(ctx, tx, ids)
	})
	if err == nil {
		d.notifyIndex()
	}
	return err
}

func (d *DB) UpdateLabels(ctx context.Context, id string, labels []string, isRead bool) error {
	if labels == nil {
		labels = []string{}
	}
	return d.withTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
UPDATE messages SET label_ids = `+listSQL+`, is_read = ? WHERE id = ?
`, encodeList(labels), isRead, id)
		return err
	})
}

func (d *DB) MissingIDs(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	have := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		var found string
		err := d.sql.QueryRowContext(ctx, "SELECT id FROM messages WHERE id = ?", id).Scan(&found)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		have[found] = struct{}{}
	}
	var missing []string
	for _, id := range ids {
		if _, ok := have[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

func (d *DB) LabelMap(ctx context.Context) (map[string]string, error) {
	rows, err := d.sql.QueryContext(ctx, "SELECT id, name FROM labels")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

func (d *DB) EnsureFTS(ctx context.Context) error {
	ok, err := d.HasFTS(ctx)
	if err != nil || ok {
		return err
	}
	if err := d.repairSearchText(ctx); err != nil {
		return err
	}
	return d.catchupIndex(ctx, false)
}
