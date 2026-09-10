package store

import (
	"context"
	"fmt"
	"strings"
)

const (
	stateSearchText = "search_text_v1"
	stateFTSIndex   = "fts_index"
	stateFTSDirty   = "fts_dirty"
	ftsIndexVer     = "search_text"
	ftsDirtyValue   = "1"
)

func (d *DB) migrateSearch(ctx context.Context) error {
	if _, err := d.sql.Exec(`ALTER TABLE messages ADD COLUMN IF NOT EXISTS search_text VARCHAR`); err != nil {
		return fmt.Errorf("search_text: %w", err)
	}
	if _, ok, err := d.GetState(ctx, stateSearchText); err != nil {
		return err
	} else if ok {
		return nil
	}
	if _, err := d.sql.Exec(`UPDATE messages SET search_text = ` + searchTextSQL + ` WHERE search_text IS NULL OR search_text = ''`); err != nil {
		return fmt.Errorf("search_text backfill: %w", err)
	}
	return d.SetState(ctx, stateSearchText, "1")
}

func (d *DB) RebuildFTS(ctx context.Context) error {
	if err := d.acquire(ctx); err != nil {
		return err
	}
	defer d.release()
	return d.rebuildFTSLocked(ctx)
}

func (d *DB) rebuildFTSLocked(ctx context.Context) error {
	if err := markFTSDirty(ctx, d.sql); err != nil {
		return err
	}
	if _, err := d.sql.ExecContext(ctx, `UPDATE messages SET search_text = `+searchTextSQL); err != nil {
		return fmt.Errorf("search_text refresh: %w", err)
	}
	if _, err := d.sql.ExecContext(ctx, `
PRAGMA create_fts_index('messages', 'id', 'search_text', overwrite=1)
`); err != nil {
		return fmt.Errorf("fts: %w", err)
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := setStateTx(ctx, tx, stateFTSIndex, ftsIndexVer); err != nil {
		return err
	}
	if err := clearStateTx(ctx, tx, stateFTSDirty); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) EnsureFTS(ctx context.Context) error {
	if err := d.acquire(ctx); err != nil {
		return err
	}
	defer d.release()
	ok, err := d.hasFTS(ctx)
	if err != nil || ok {
		return err
	}
	return d.rebuildFTSLocked(ctx)
}

func (d *DB) hasFTS(ctx context.Context) (bool, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `
SELECT count(*) FROM duckdb_schemas() WHERE schema_name = 'fts_main_messages'
`).Scan(&n)
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	ver, _, err := d.GetState(ctx, stateFTSIndex)
	if err != nil {
		return false, err
	}
	if ver != ftsIndexVer {
		return false, nil
	}
	dirty, ok, err := d.GetState(ctx, stateFTSDirty)
	if err != nil {
		return false, err
	}
	if ok && dirty == ftsDirtyValue {
		return false, nil
	}
	return true, nil
}

func (d *DB) HasFTS(ctx context.Context) (bool, error) {
	return d.hasFTS(ctx)
}

func markFTSDirty(ctx context.Context, ex execer) error {
	_, err := ex.ExecContext(ctx, setStateSQL, stateFTSDirty, ftsDirtyValue)
	return err
}

func refreshSearchTextTx(ctx context.Context, ex execer, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("UPDATE messages SET search_text = ")
	b.WriteString(searchTextSQL)
	b.WriteString(" WHERE id IN (")
	args := make([]any, len(ids))
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('?')
		args[i] = id
	}
	b.WriteByte(')')
	_, err := ex.ExecContext(ctx, b.String(), args...)
	return err
}
