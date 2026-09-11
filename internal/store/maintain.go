package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
)

const (
	indexBatchMsgs    = 256
	indexBatchBytes   = 8 << 20
	indexCatchupEvery = 2 * time.Second
)

func (d *DB) StartMaintenance(ctx context.Context) {
	if d.maintStarted.Swap(true) {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	d.maintCancel = cancel
	d.maintWG.Add(1)
	go d.maintainLoop(ctx)
}

func (d *DB) stopMaint() {
	if d.maintCancel != nil {
		d.maintCancel()
	}
	d.maintWG.Wait()
}

func (d *DB) notifyIndex() {
	if d.maintCh == nil {
		return
	}
	select {
	case d.maintCh <- struct{}{}:
	default:
	}
}

// maintainLoop is the only writer of the live SQLite index besides publishFile.
func (d *DB) maintainLoop(ctx context.Context) {
	defer d.maintWG.Done()
	tick := time.NewTicker(indexCatchupEvery)
	defer tick.Stop()
	for {
		if err := d.catchupIndex(ctx, false); err != nil && ctx.Err() == nil {
			d.indexErr(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-d.maintCh:
		case <-tick.C:
		}
	}
}

func (d *DB) indexErr(err error) {
	if d.IndexLog != nil {
		d.IndexLog("search index: %v", err)
	}
}

func (d *DB) catchupIndex(ctx context.Context, force bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fresh, err := d.readFreshness(ctx, nil)
	if err != nil {
		return err
	}
	if fresh.accelOff {
		return nil
	}
	if fresh.repair {
		if err := d.repairSearchText(ctx); err != nil {
			return err
		}
	}
	d.indexMu.Lock()
	defer d.indexMu.Unlock()
	conn, err := d.reserved(ctx, &d.maint)
	if err != nil {
		return fmt.Errorf("maintenance connection unavailable: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN TRANSACTION READ ONLY"); err != nil {
		if deadConn(err) {
			d.dropReserved(&d.maint)
			conn, err = d.reserved(ctx, &d.maint)
			if err != nil {
				return err
			}
			_, err = conn.ExecContext(ctx, "BEGIN TRANSACTION READ ONLY")
		}
		if err != nil {
			return err
		}
	}
	defer func() {
		if err := rollbackConn(conn); err != nil {
			discardConn(conn)
			d.dropReserved(&d.maint)
		}
	}()
	fresh, err = d.readFreshness(ctx, conn)
	if err != nil {
		return err
	}
	if fresh.accelOff {
		return nil
	}
	meta, metaErr := d.idx.peek(ctx)
	d.idx.mu.Lock()
	broken := d.idx.broken
	d.idx.mu.Unlock()
	needBuild := force || broken || metaErr != nil || !meta.Usable || meta.Identity != fresh.identity || meta.Epoch != fresh.epoch || meta.Version != searchIndexVer || !meta.Ready || meta.Watermark > fresh.corpus
	if needBuild {
		return d.fullBuild(ctx, conn, fresh)
	}
	if meta.Watermark == fresh.corpus {
		return nil
	}
	return d.applyDelta(ctx, conn, meta.Watermark, fresh.corpus)
}

func (d *DB) applyDelta(ctx context.Context, conn *sql.Conn, watermark, high int64) error {
	var cur idCursor
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ids, err := slimIDs(ctx, conn, "search_revision > ? AND search_revision <= ?", []any{watermark, high}, cur, indexBatchMsgs)
		if err != nil {
			return err
		}
		ids, err = clipIDsByBytes(ctx, conn, ids, true)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			break
		}
		rows, err := loadIndexRows(ctx, conn, ids)
		if err != nil {
			return err
		}
		if err := d.idx.apply(ctx, rows); err != nil {
			return err
		}
		cur.next(ids[len(ids)-1])
	}
	return d.idx.setWatermark(ctx, high)
}

func (d *DB) fullBuild(ctx context.Context, conn *sql.Conn, fresh freshness) error {
	d.building.Store(true)
	defer d.building.Store(false)
	if err := prepareSearchDir(d.idx.dir); err != nil {
		return err
	}
	stage := filepath.Join(d.idx.dir, indexBuildName)
	_ = os.Remove(stage)
	removeIndexSidecars(stage)
	sdb, err := sql.Open("sqlite", sqliteDSN(stage))
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = sdb.Close()
		if !ok {
			_ = os.Remove(stage)
			removeIndexSidecars(stage)
		}
	}()
	if err := initIndexSchema(ctx, sdb); err != nil {
		return err
	}
	var cur idCursor
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ids, err := slimIDs(ctx, conn, "", nil, cur, indexBatchMsgs)
		if err != nil {
			return err
		}
		ids, err = clipIDsByBytes(ctx, conn, ids, true)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			break
		}
		rows, err := loadIndexRows(ctx, conn, ids)
		if err != nil {
			return err
		}
		tx, err := sdb.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := applyRowsTx(ctx, tx, rows); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		cur.next(ids[len(ids)-1])
	}
	if err := writeCacheMeta(ctx, sdb, fresh, fresh.corpus, true); err != nil {
		return err
	}
	if _, err := sdb.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return err
	}
	if err := sdb.Close(); err != nil {
		return err
	}
	if err := d.idx.publishFile(ctx, stage); err != nil {
		return err
	}
	ok = true
	return inspectCacheDir(d.idx.dir, true)
}

type idCursor struct {
	set bool
	id  string
}

func (c *idCursor) next(id string) {
	c.set = true
	c.id = id
}

func slimIDs(ctx context.Context, q queryer, extra string, args []any, cur idCursor, limit int) ([]string, error) {
	var b strings.Builder
	b.WriteString("SELECT id FROM messages")
	var parts []string
	if extra != "" {
		parts = append(parts, extra)
	}
	if cur.set {
		parts = append(parts, "id > ?")
		args = append(args, cur.id)
	}
	if len(parts) > 0 {
		b.WriteString(" WHERE ")
		b.WriteString(strings.Join(parts, " AND "))
	}
	b.WriteString(" ORDER BY id LIMIT ?")
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, b.String(), args...)
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

func textLenSQL(useCached bool) string {
	if useCached {
		return "COALESCE(strlen(COALESCE(search_text, '')), 0)"
	}
	return "COALESCE(strlen((" + searchTextSQL + ")), 0)"
}

func clipIDsByBytes(ctx context.Context, q queryer, ids []string, useCached bool) ([]string, error) {
	return clipIDsByBytesN(ctx, q, ids, indexBatchBytes, useCached)
}

func clipIDsByBytesN(ctx context.Context, q queryer, ids []string, maxBytes int, useCached bool) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var b strings.Builder
	// Cached path uses search_text only. Repair sizes the live expression so a
	// stale or NULL cache cannot hide a large body. One row larger than the
	// bound is still taken.
	b.WriteString("SELECT id, ")
	b.WriteString(textLenSQL(useCached))
	b.WriteString(" FROM messages WHERE id IN ")
	args := writeIn(&b, nil, ids)
	rows, err := q.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	size := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		size[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var (
		out  []string
		used int
	)
	for _, id := range ids {
		n := size[id]
		if len(out) > 0 && used+n > maxBytes {
			break
		}
		out = append(out, id)
		used += n
	}
	return out, nil
}

func loadIndexRows(ctx context.Context, conn *sql.Conn, ids []string) ([]indexRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var b strings.Builder
	b.WriteString(`
SELECT id, lower(COALESCE(search_text, '')), CAST(epoch_us(internal_date) AS BIGINT),
       COALESCE(search_revision, 0), is_deleted
FROM messages WHERE id IN `)
	args := writeIn(&b, nil, ids)
	rows, err := conn.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []indexRow
	for rows.Next() {
		var r indexRow
		if err := rows.Scan(&r.ID, &r.Text, &r.Date, &r.Rev, &r.Deleted); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func prepareSearchDir(dir string) error {
	fi, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return privfile.MkdirPrivate(dir)
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("search directory is a symlink")
	}
	if !fi.IsDir() {
		return fmt.Errorf("search path is not a directory")
	}
	return inspectCacheDir(dir, false)
}
