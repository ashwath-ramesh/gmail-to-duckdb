package searchbench

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func SeedRange(ctx context.Context, db *sql.DB, start, end, rev int) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO messages (
  id, thread_id, history_id, internal_date,
  from_name, from_email, to_emails, cc_emails,
  subject, snippet, body, size_bytes, label_ids,
  is_read, is_outgoing, is_deleted, has_body, body_fetched, synced_at,
  search_text, search_revision
)
SELECT
  printf('id-%%06d', i),
  't',
  10,
  TIMESTAMP '2020-01-01' + (i || ' minutes')::INTERVAL,
  'Alice',
  'alice@example.com',
  ['bob@example.com'],
  ['cc@example.com'],
  s.subject,
  s.snippet,
  s.body,
  strlen(s.body) + strlen(s.subject) + strlen(s.snippet),
  ['INBOX'],
  (i %% 2 = 0),
  false,
  false,
  true,
  true,
  TIMESTAMP '2020-01-01',
  trim(concat_ws(' ', 'Alice', 'alice@example.com', 'bob@example.com', 'cc@example.com', s.subject, s.snippet, s.body)),
  %d
FROM (
  SELECT
    i,
    'subj ' || (i %% 17)::VARCHAR || ' token' || i::VARCHAR AS subject,
    'snip ' || (i %% 31)::VARCHAR AS snippet,
    repeat('sharedword ' || (i %% 97)::VARCHAR || ' alpha beta token' || i::VARCHAR || ' ', 16 + (i %% 200))
      || CASE WHEN i %% 500 = 0 THEN ' invoice leftover' ELSE '' END AS body
  FROM range(%d, %d) AS t(i)
) s
`, rev, start, end))
	return err
}

func CorpusBytes(ctx context.Context, db *sql.DB) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx, "SELECT COALESCE(SUM(strlen(search_text)), 0) FROM messages").Scan(&n)
	return n, err
}

func DirSize(root string) int64 {
	var n int64
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && info.Mode().IsRegular() {
			n += info.Size()
		}
		return nil
	})
	return n
}

func P95(in []time.Duration) time.Duration {
	if len(in) == 0 {
		return 0
	}
	out := append([]time.Duration(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	idx := int(math.Ceil(0.95*float64(len(out)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(out) {
		idx = len(out) - 1
	}
	return out[idx]
}

func Median(in []time.Duration) time.Duration {
	if len(in) == 0 {
		return 0
	}
	out := append([]time.Duration(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	n := len(out)
	if n%2 == 1 {
		return out[n/2]
	}
	return (out[n/2-1] + out[n/2]) / 2
}

func MS(d time.Duration) float64 {
	return float64(d.Nanoseconds()) / 1e6
}

func RSSKb() int64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			return 0
		}
		n, _ := strconv.ParseInt(f[1], 10, 64)
		return n
	}
	return 0
}

func MailboxPath() (string, bool, error) {
	p := strings.TrimSpace(os.Getenv("GMAIL_BENCH_FIXTURE"))
	if p == "" {
		return "", false, nil
	}
	st, err := os.Stat(p)
	if err == nil {
		if st.IsDir() {
			return filepath.Join(p, "bench.duckdb"), true, nil
		}
		return p, true, nil
	}
	if os.IsNotExist(err) && strings.EqualFold(filepath.Ext(p), ".duckdb") {
		return p, true, nil
	}
	return "", true, fmt.Errorf("fixture %s: %w", p, err)
}

func NextIDRange(ctx context.Context, db *sql.DB, n int) (int, int, error) {
	var c int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&c); err != nil {
		return 0, 0, err
	}
	var max sql.NullInt64
	err := db.QueryRowContext(ctx, `
SELECT MAX(TRY_CAST(substr(id, 4) AS INTEGER))
FROM messages
WHERE id LIKE 'id-%'`).Scan(&max)
	if err != nil {
		return 0, 0, err
	}
	start := c
	if max.Valid && int(max.Int64)+1 > start {
		start = int(max.Int64) + 1
	}
	return start, start + n, nil
}

func MarkerCount(ctx context.Context, db *sql.DB, token string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE COALESCE(search_text, '') ILIKE ? AND NOT is_deleted", "%"+token+"%").Scan(&n)
	return n, err
}

func SeedMarkers(ctx context.Context, db *sql.DB, token string, n, rev int) error {
	have, err := MarkerCount(ctx, db, token)
	if err != nil || have >= n {
		return err
	}
	start, end, err := NextIDRange(ctx, db, n-have)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO messages (
  id, thread_id, history_id, internal_date,
  from_name, from_email, to_emails, cc_emails,
  subject, snippet, body, size_bytes, label_ids,
  is_read, is_outgoing, is_deleted, has_body, body_fetched, synced_at,
  search_text, search_revision
)
SELECT
  printf('id-%%06d', i),
  't',
  10,
  TIMESTAMP '2026-01-01' + (i || ' minutes')::INTERVAL,
  'Marker',
  'marker@example.com',
  ['bob@example.com'],
  ['cc@example.com'],
  '%s ' || i::VARCHAR,
  'snip',
  '%s body ' || i::VARCHAR,
  32,
  ['INBOX'],
  (i %% 2 = 0),
  false,
  false,
  true,
  true,
  TIMESTAMP '2026-01-01',
  trim(concat_ws(' ', 'Marker', 'marker@example.com', '%s', '%s body', i::VARCHAR)),
  %d
FROM range(%d, %d) AS t(i)
`, token, token, token, token, rev, start, end))
	return err
}

func CorpusRev(ctx context.Context, db *sql.DB) (int64, error) {
	var v string
	err := db.QueryRowContext(ctx, "SELECT value FROM sync_state WHERE key = 'search_corpus_revision'").Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

func SearchDir(dbPath string) string {
	return dbPath + ".search"
}

func RemoveSearchCache(dbPath string) error {
	dir := SearchDir(dbPath)
	fi, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("search cache is a symlink")
	}
	if !fi.IsDir() {
		return fmt.Errorf("search cache is not a directory")
	}
	return os.RemoveAll(dir)
}
