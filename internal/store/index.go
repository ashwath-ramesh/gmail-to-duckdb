package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

const (
	stateSearchText       = "search_text_v1"
	stateFTSDirty         = "fts_dirty"
	stateCorpusRevision   = "search_corpus_revision"
	stateSearchEpoch      = "search_norm_epoch"
	stateSearchCacheID    = "search_cache_id"
	stateSearchAccel      = "search_accel"
	ftsDirtyValue         = "1"
	searchAccelOff        = "0"
	searchIndexVer        = "trigram-v1"
	IndexStateReady       = "ready"
	IndexStatePending     = "pending"
	IndexStateBuilding    = "building"
	IndexStateRepair      = "repair"
	IndexStateDisabled    = "disabled"
	IndexStateUnavailable = "unavailable"
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
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := initSearchMetaTx(ctx, tx); err != nil {
		return err
	}
	if err := markLegacySearchRepair(ctx, tx); err != nil {
		return err
	}
	if err := setStateTx(ctx, tx, stateSearchText, "1"); err != nil {
		return err
	}
	return tx.Commit()
}

func initSearchMetaTx(ctx context.Context, tx *sql.Tx) error {
	if _, ok, err := getStateTx(ctx, tx, stateCorpusRevision); err != nil {
		return err
	} else if !ok {
		if err := setStateTx(ctx, tx, stateCorpusRevision, "0"); err != nil {
			return err
		}
	}
	if _, ok, err := getStateTx(ctx, tx, stateSearchEpoch); err != nil {
		return err
	} else if !ok {
		if err := setStateTx(ctx, tx, stateSearchEpoch, "1"); err != nil {
			return err
		}
	}
	if _, ok, err := getStateTx(ctx, tx, stateSearchCacheID); err != nil {
		return err
	} else if !ok {
		id, err := newCacheID()
		if err != nil {
			return err
		}
		if err := setStateTx(ctx, tx, stateSearchCacheID, id); err != nil {
			return err
		}
	}
	return nil
}

func newCacheID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (d *DB) RebuildFTS(ctx context.Context) error {
	if err := d.repairSearchText(ctx); err != nil {
		return err
	}
	return d.catchupIndex(ctx, true)
}

func (d *DB) EnsureFTS(ctx context.Context) error {
	ok, err := d.hasFTS(ctx)
	if err != nil || ok {
		return err
	}
	if err := d.repairSearchText(ctx); err != nil {
		return err
	}
	return d.catchupIndex(ctx, false)
}

func (d *DB) HasFTS(ctx context.Context) (bool, error) {
	return d.hasFTS(ctx)
}

func (d *DB) hasFTS(ctx context.Context) (bool, error) {
	st, err := d.SearchIndexState(ctx)
	if err != nil {
		return false, err
	}
	return st == IndexStateReady, nil
}

func (d *DB) SearchIndexState(ctx context.Context) (string, error) {
	if d.building.Load() {
		return IndexStateBuilding, nil
	}
	accel, _, err := d.GetState(ctx, stateSearchAccel)
	if err != nil {
		return "", err
	}
	if accel == searchAccelOff {
		return IndexStateDisabled, nil
	}
	dirty, ok, err := d.GetState(ctx, stateFTSDirty)
	if err != nil {
		return "", err
	}
	if ok && dirty == ftsDirtyValue {
		return IndexStateRepair, nil
	}
	fresh, err := d.readFreshness(ctx, nil)
	if err != nil {
		return "", err
	}
	meta, err := d.idx.peek(ctx)
	if err != nil || !meta.Usable {
		return IndexStateUnavailable, nil
	}
	if meta.Identity != fresh.identity || meta.Epoch != fresh.epoch || meta.Version != searchIndexVer {
		return IndexStateUnavailable, nil
	}
	if !meta.Ready || meta.Watermark > fresh.corpus {
		return IndexStateUnavailable, nil
	}
	if meta.Watermark < fresh.corpus {
		return IndexStatePending, nil
	}
	return IndexStateReady, nil
}

func markFTSDirty(ctx context.Context, ex execer) error {
	_, err := ex.ExecContext(ctx, setStateSQL, stateFTSDirty, ftsDirtyValue)
	return err
}

func markLegacySearchRepair(ctx context.Context, tx *sql.Tx) error {
	var n int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	return markFTSDirty(ctx, tx)
}

func invalidateSearchTx(ctx context.Context, tx *sql.Tx) error {
	if err := markFTSDirty(ctx, tx); err != nil {
		return err
	}
	epoch := int64(1)
	if v, ok, err := getStateTx(ctx, tx, stateSearchEpoch); err != nil {
		return err
	} else if ok {
		epoch, _ = strconv.ParseInt(v, 10, 64)
		epoch++
	}
	if err := setStateTx(ctx, tx, stateSearchEpoch, strconv.FormatInt(epoch, 10)); err != nil {
		return err
	}
	id, err := newCacheID()
	if err != nil {
		return err
	}
	return setStateTx(ctx, tx, stateSearchCacheID, id)
}

func (d *DB) DisableSearchAccel(ctx context.Context) error {
	err := d.withTx(ctx, func(tx *sql.Tx) error {
		if err := setStateTx(ctx, tx, stateSearchAccel, searchAccelOff); err != nil {
			return err
		}
		return invalidateSearchTx(ctx, tx)
	})
	if err == nil {
		d.notifyIndex()
	}
	return err
}

type freshness struct {
	corpus   int64
	epoch    string
	identity string
	repair   bool
	accelOff bool
}

func (d *DB) readFreshness(ctx context.Context, q querier) (freshness, error) {
	if q == nil {
		q = d.sql
	}
	var f freshness
	var err error
	f.corpus, err = queryIntState(ctx, q, stateCorpusRevision)
	if err != nil {
		return f, err
	}
	f.epoch, _, err = queryState(ctx, q, stateSearchEpoch)
	if err != nil {
		return f, err
	}
	f.identity, _, err = queryState(ctx, q, stateSearchCacheID)
	if err != nil {
		return f, err
	}
	dirty, ok, err := queryState(ctx, q, stateFTSDirty)
	if err != nil {
		return f, err
	}
	f.repair = ok && dirty == ftsDirtyValue
	accel, _, err := queryState(ctx, q, stateSearchAccel)
	if err != nil {
		return f, err
	}
	f.accelOff = accel == searchAccelOff
	return f, nil
}

type querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type queryer interface {
	querier
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func queryState(ctx context.Context, q querier, key string) (string, bool, error) {
	var v string
	err := q.QueryRowContext(ctx, "SELECT value FROM sync_state WHERE key = ?", key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func queryIntState(ctx context.Context, q querier, key string) (int64, error) {
	v, ok, err := queryState(ctx, q, key)
	if err != nil || !ok || v == "" {
		return 0, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func nextCorpusTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	n, err := queryIntState(ctx, tx, stateCorpusRevision)
	if err != nil {
		return 0, err
	}
	n++
	if err := setStateTx(ctx, tx, stateCorpusRevision, strconv.FormatInt(n, 10)); err != nil {
		return 0, err
	}
	return n, nil
}

func touchSearchTx(ctx context.Context, tx *sql.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	rev, err := nextCorpusTx(ctx, tx)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("UPDATE messages SET search_text = ")
	b.WriteString(searchTextSQL)
	b.WriteString(", search_revision = ? WHERE id IN ")
	args := []any{rev}
	args = writeIn(&b, args, ids)
	_, err = tx.ExecContext(ctx, b.String(), args...)
	return err
}

func touchRevisionTx(ctx context.Context, tx *sql.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	rev, err := nextCorpusTx(ctx, tx)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("UPDATE messages SET search_revision = ? WHERE id IN ")
	args := []any{rev}
	args = writeIn(&b, args, ids)
	_, err = tx.ExecContext(ctx, b.String(), args...)
	return err
}

func writeIn(b *strings.Builder, args []any, ids []string) []any {
	b.WriteByte('(')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('?')
		args = append(args, id)
	}
	b.WriteByte(')')
	return args
}

func (d *DB) repairSearchText(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		epoch, _, err := d.GetState(ctx, stateSearchEpoch)
		if err != nil {
			return err
		}
		var page idCursor
		restart := false
		cleared := false
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			err := d.withTx(ctx, func(tx *sql.Tx) error {
				cur, _, err := getStateTx(ctx, tx, stateSearchEpoch)
				if err != nil {
					return err
				}
				if cur != epoch {
					restart = true
					return nil
				}
				ids, err := slimIDs(ctx, tx, "", nil, page, indexBatchMsgs)
				if err != nil {
					return err
				}
				ids, err = clipIDsByBytes(ctx, tx, ids, false)
				if err != nil {
					return err
				}
				if len(ids) == 0 {
					again, _, err := getStateTx(ctx, tx, stateSearchEpoch)
					if err != nil {
						return err
					}
					if again != epoch {
						restart = true
						return nil
					}
					cleared = true
					return clearStateTx(ctx, tx, stateFTSDirty)
				}
				stale, err := staleSearchIDs(ctx, tx, ids)
				if err != nil {
					return err
				}
				if err := touchSearchTx(ctx, tx, stale); err != nil {
					return err
				}
				page.next(ids[len(ids)-1])
				return nil
			})
			if err != nil {
				return err
			}
			if restart || cleared {
				break
			}
		}
		if cleared {
			return nil
		}
	}
}

func staleSearchIDs(ctx context.Context, q queryer, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var b strings.Builder
	b.WriteString("SELECT id FROM messages WHERE id IN ")
	args := writeIn(&b, nil, ids)
	b.WriteString(" AND search_text IS DISTINCT FROM (")
	b.WriteString(searchTextSQL)
	b.WriteByte(')')
	rows, err := q.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
