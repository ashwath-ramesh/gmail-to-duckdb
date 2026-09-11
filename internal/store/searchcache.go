package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
	_ "modernc.org/sqlite"
)

const (
	indexFileName  = "index.sqlite"
	indexBuildName = "index.build.sqlite"
	metaIdentity   = "identity"
	metaEpoch      = "epoch"
	metaVersion    = "version"
	metaWatermark  = "watermark"
	metaReady      = "ready"
)

var cacheSidecarSuffixes = []string{"", "-wal", "-shm", "-journal"}

type searchCache struct {
	dir            string
	live           string
	mu             sync.Mutex
	cond           *sync.Cond
	sql            *sql.DB
	pins           int
	replacing      bool
	broken         bool
	holdCandidates func()
}

type cacheMeta struct {
	Identity  string
	Epoch     string
	Version   string
	Watermark int64
	Ready     bool
	Usable    bool
}

type indexRow struct {
	ID      string
	Date    int64
	Rev     int64
	Deleted bool
	Text    string
}

type cand struct {
	ID   string
	Date int64
}

func SearchDir(dbPath string) string {
	return dbPath + ".search"
}

func newSearchCache(dbPath string) *searchCache {
	dir := SearchDir(dbPath)
	c := &searchCache{dir: dir, live: filepath.Join(dir, indexFileName)}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *searchCache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.replacing = true
	c.cond.Broadcast()
	for c.pins > 0 {
		c.cond.Wait()
	}
	return c.closeLocked()
}

func (c *searchCache) closeLocked() error {
	if c.sql == nil {
		return nil
	}
	_, _ = c.sql.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	err := c.sql.Close()
	c.sql = nil
	return err
}

func (c *searchCache) peek(ctx context.Context) (cacheMeta, error) {
	_, meta, err := c.snapshot(ctx, "")
	return meta, err
}

func (c *searchCache) snapshot(ctx context.Context, match string) ([]cand, cacheMeta, error) {
	conn, meta, err := c.pin(ctx)
	if err != nil {
		return nil, cacheMeta{}, err
	}
	defer c.unpin(conn)
	if match == "" {
		return nil, meta, nil
	}
	cands, err := c.candidates(ctx, conn, match)
	if err != nil {
		return nil, meta, err
	}
	return cands, meta, nil
}

func (c *searchCache) pin(ctx context.Context) (*sql.Conn, cacheMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, cacheMeta{}, err
	}
	if err := c.openLive(ctx); err != nil {
		return nil, cacheMeta{}, err
	}
	c.mu.Lock()
	if c.replacing || c.broken || c.sql == nil {
		c.mu.Unlock()
		return nil, cacheMeta{}, errIndexUnavailable
	}
	db := c.sql
	c.pins++
	c.mu.Unlock()
	conn, err := db.Conn(ctx)
	if err != nil {
		c.releasePin()
		return nil, cacheMeta{}, err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		_ = conn.Close()
		c.releasePin()
		return nil, cacheMeta{}, err
	}
	meta, err := readCacheMeta(ctx, conn)
	if err != nil {
		c.unpin(conn)
		return nil, cacheMeta{}, err
	}
	c.mu.Lock()
	broken := c.broken
	c.mu.Unlock()
	if broken {
		c.unpin(conn)
		return nil, cacheMeta{}, errIndexUnavailable
	}
	return conn, meta, nil
}

func (c *searchCache) unpin(conn *sql.Conn) {
	if conn == nil {
		return
	}
	_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
	_ = conn.Close()
	c.releasePin()
}

var errIndexUnavailable = errors.New("search index unavailable")

func (c *searchCache) releasePin() {
	c.mu.Lock()
	if c.pins > 0 {
		c.pins--
	}
	c.cond.Broadcast()
	c.mu.Unlock()
}

func (c *searchCache) markBroken() {
	c.mu.Lock()
	c.broken = true
	c.mu.Unlock()
}

func (c *searchCache) candidates(ctx context.Context, conn *sql.Conn, match string) ([]cand, error) {
	if c.holdCandidates != nil {
		c.holdCandidates()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := conn.QueryContext(ctx, `
SELECT d.id, d.dt FROM fts
JOIN docs d ON d.rowid = fts.rowid
WHERE fts MATCH ?
`, match)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cand
	for rows.Next() {
		var cnd cand
		if err := rows.Scan(&cnd.ID, &cnd.Date); err != nil {
			return nil, err
		}
		out = append(out, cnd)
	}
	return out, rows.Err()
}

func (c *searchCache) openLive(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.replacing {
		return errIndexUnavailable
	}
	if c.sql != nil {
		return nil
	}
	if err := inspectCacheDir(c.dir, true); err != nil {
		return err
	}
	return c.openPathLocked(c.live)
}

func (c *searchCache) openPathLocked(path string) error {
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	if _, err := db.Exec("SELECT 1 FROM meta LIMIT 1"); err != nil {
		_ = db.Close()
		return err
	}
	c.sql = db
	c.broken = false
	return nil
}

func (c *searchCache) apply(ctx context.Context, rows []indexRow) error {
	if len(rows) == 0 {
		return nil
	}
	if err := c.openLive(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	db := c.sql
	c.mu.Unlock()
	if db == nil {
		return fmt.Errorf("search index closed")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := applyRowsTx(ctx, tx, rows); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return inspectCacheDir(c.dir, true)
}

func (c *searchCache) setWatermark(ctx context.Context, w int64) error {
	if err := c.openLive(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	db := c.sql
	c.mu.Unlock()
	if db == nil {
		return fmt.Errorf("search index closed")
	}
	_, err := db.ExecContext(ctx, `INSERT INTO meta(k, v) VALUES (?, ?)
ON CONFLICT(k) DO UPDATE SET v = excluded.v`, metaWatermark, strconv.FormatInt(w, 10))
	return err
}

func (c *searchCache) publishFile(ctx context.Context, stage string) error {
	if err := privfile.Harden(stage); err != nil {
		return err
	}
	if err := privfile.Check(stage); err != nil {
		return err
	}
	c.mu.Lock()
	c.replacing = true
	c.cond.Broadcast()
	stop := context.AfterFunc(ctx, func() {
		c.mu.Lock()
		c.cond.Broadcast()
		c.mu.Unlock()
	})
	for c.pins > 0 {
		if err := ctx.Err(); err != nil {
			c.replacing = false
			c.cond.Broadcast()
			stop()
			c.mu.Unlock()
			return err
		}
		c.cond.Wait()
	}
	stop()
	_ = c.closeLocked()
	removeIndexSidecars(c.live)
	err := privfile.Replace(stage, c.live)
	if err == nil {
		err = inspectCacheDir(c.dir, true)
	}
	if err == nil {
		err = c.openPathLocked(c.live)
	}
	c.broken = err != nil
	c.replacing = false
	c.cond.Broadcast()
	c.mu.Unlock()
	removeIndexSidecars(stage)
	return err
}

func applyRowsTx(ctx context.Context, tx *sql.Tx, rows []indexRow) error {
	for _, r := range rows {
		r.Text = indexText(r.Text)
		if r.Deleted || r.Text == "" {
			if err := deleteDocTx(ctx, tx, r.ID); err != nil {
				return err
			}
			continue
		}
		if err := upsertDocTx(ctx, tx, r); err != nil {
			return err
		}
	}
	return nil
}

func deleteDocTx(ctx context.Context, tx *sql.Tx, id string) error {
	var rowid int64
	err := tx.QueryRowContext(ctx, "SELECT rowid FROM docs WHERE id = ?", id).Scan(&rowid)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM fts WHERE rowid = ?", rowid); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM docs WHERE rowid = ?", rowid)
	return err
}

func upsertDocTx(ctx context.Context, tx *sql.Tx, r indexRow) error {
	var rowid int64
	err := tx.QueryRowContext(ctx, "SELECT rowid FROM docs WHERE id = ?", r.ID).Scan(&rowid)
	if err == sql.ErrNoRows {
		res, err := tx.ExecContext(ctx, "INSERT INTO docs(id, dt, rev) VALUES (?, ?, ?)", r.ID, r.Date, r.Rev)
		if err != nil {
			return err
		}
		rowid, err = res.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO fts(rowid, t) VALUES (?, ?)", rowid, r.Text)
		return err
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM fts WHERE rowid = ?", rowid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO fts(rowid, t) VALUES (?, ?)", rowid, r.Text); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE docs SET dt = ?, rev = ? WHERE rowid = ?", r.Date, r.Rev, rowid)
	return err
}

func initIndexSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS docs (
  rowid INTEGER PRIMARY KEY,
  id TEXT NOT NULL UNIQUE,
  dt INTEGER NOT NULL,
  rev INTEGER NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS fts USING fts5(
  t,
  tokenize='trigram case_sensitive 1',
  content='',
  contentless_delete=1,
  detail=none
);
`)
	return err
}

func writeCacheMeta(ctx context.Context, db *sql.DB, fresh freshness, watermark int64, ready bool) error {
	readyV := "0"
	if ready {
		readyV = "1"
	}
	pairs := [][2]string{
		{metaIdentity, fresh.identity},
		{metaEpoch, fresh.epoch},
		{metaVersion, searchIndexVer},
		{metaWatermark, strconv.FormatInt(watermark, 10)},
		{metaReady, readyV},
	}
	for _, p := range pairs {
		if _, err := db.ExecContext(ctx, `INSERT INTO meta(k, v) VALUES (?, ?)
ON CONFLICT(k) DO UPDATE SET v = excluded.v`, p[0], p[1]); err != nil {
			return err
		}
	}
	return nil
}

func readCacheMeta(ctx context.Context, q querier) (cacheMeta, error) {
	var m cacheMeta
	get := func(k string) (string, error) {
		var v string
		err := q.QueryRowContext(ctx, "SELECT v FROM meta WHERE k = ?", k).Scan(&v)
		if err == sql.ErrNoRows {
			return "", nil
		}
		return v, err
	}
	var err error
	if m.Identity, err = get(metaIdentity); err != nil {
		return m, err
	}
	if m.Epoch, err = get(metaEpoch); err != nil {
		return m, err
	}
	if m.Version, err = get(metaVersion); err != nil {
		return m, err
	}
	w, err := get(metaWatermark)
	if err != nil {
		return m, err
	}
	if w != "" {
		m.Watermark, err = strconv.ParseInt(w, 10, 64)
		if err != nil {
			return m, err
		}
	}
	ready, err := get(metaReady)
	if err != nil {
		return m, err
	}
	m.Ready = ready == "1"
	m.Usable = m.Identity != "" && m.Epoch != "" && m.Version == searchIndexVer && m.Ready
	return m, nil
}

func sqliteDSN(path string) string {
	p := filepath.ToSlash(path)
	if filepath.IsAbs(path) && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	return u.String() + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)"
}

func transientIndexErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errIndexUnavailable) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "busy") || strings.Contains(s, "locked") || strings.Contains(s, "interrupt")
}

func ftsMatch(grams []string) (string, bool) {
	if len(grams) == 0 {
		return "", false
	}
	var b strings.Builder
	for i, g := range grams {
		if !safeGram(g) {
			return "", false
		}
		if i > 0 {
			b.WriteString(" AND ")
		}
		b.WriteString(quoteFTS(g))
	}
	return b.String(), true
}

func quoteFTS(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func trigrams(s string) []string {
	rs := []rune(s)
	if len(rs) < 3 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(rs)-2)
	for i := 0; i+3 <= len(rs); i++ {
		g := string(rs[i : i+3])
		if _, ok := seen[g]; ok {
			continue
		}
		seen[g] = struct{}{}
		out = append(out, g)
	}
	return out
}

func safeGram(g string) bool {
	return g != "" && utf8.ValidString(g) && strings.IndexByte(g, 0) < 0
}

func indexText(s string) string {
	return strings.ReplaceAll(s, "\x00", " ")
}

func inspectCacheDir(dir string, requireLive bool) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("search directory is a symlink")
	}
	if !fi.IsDir() {
		return fmt.Errorf("search path is not a directory")
	}
	if err := privfile.HardenDir(dir); err != nil {
		return err
	}
	if err := privfile.CheckDir(dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if err := inspectOwnedRegular(p); err != nil {
			return err
		}
	}
	if requireLive {
		return inspectOwnedRegular(filepath.Join(dir, indexFileName))
	}
	return nil
}

func inspectOwnedRegular(path string) error {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return err
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", filepath.Base(path))
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	if err := privfile.Harden(path); err != nil {
		return err
	}
	return privfile.Check(path)
}

func inspectOwnedRegularIfExists(path string) error {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return inspectOwnedRegular(path)
}

func removeIndexSidecars(path string) {
	for _, suf := range cacheSidecarSuffixes {
		if suf == "" {
			continue
		}
		_ = os.Remove(path + suf)
	}
}
