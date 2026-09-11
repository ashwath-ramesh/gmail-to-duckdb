package store

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/search"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/searchbench"
)

func TestSearchSparseCandidatesVerifyPlan(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	base := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)
	filler := strings.Repeat("sharedword alpha beta ", 48)
	const n, step = 320, 40
	var msgs []Message
	var want []string
	for i := 0; i < n; i++ {
		m := sample("s"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Minute))
		m.Body = filler
		m.HasBody = true
		m.IsRead = true
		if i%step == 0 {
			m.Subject = "note invoice leftover"
			m.IsRead = i%80 != 0
			want = append(want, m.ID)
		} else {
			m.Subject = "note"
		}
		msgs = append(msgs, m)
	}
	if err := db.UpsertMessages(ctx, msgs); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}

	got := indexedIDs(t, db, `"invoice leftover"`)
	if strings.Join(got, ",") != strings.Join(newestFirst(want), ",") {
		t.Fatalf("indexed sparse hits %#v want %#v", got, newestFirst(want))
	}

	cands := append(append([]string{}, newestFirst(want)...), "s1", "s2", "s3")
	parsed, err := search.Parse(`"invoice leftover"`)
	if err != nil {
		t.Fatal(err)
	}
	f := ListFilter{Query: `"invoice leftover"`, Limit: len(cands)}
	page, err := db.queryList(ctx, nil, f, parsed, parsed.Terms, true, len(cands), 0, false, time.Time{}, "", cands...)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]struct{}, len(page))
	for _, m := range page {
		seen[m.ID] = struct{}{}
	}
	var ordered []string
	for _, id := range cands {
		if _, ok := seen[id]; ok {
			ordered = append(ordered, id)
		}
	}
	if strings.Join(ordered, ",") != strings.Join(newestFirst(want), ",") {
		t.Fatalf("verify order %#v", ordered)
	}

	unread := ids(mustIndexed(t, db, `"invoice leftover"`, ListFilter{Unread: true, Limit: 50}))
	if strings.Join(unread, ",") != "s240,s160,s80,s0" {
		t.Fatalf("unread %#v", unread)
	}

	plan := explainList(t, db, f, parsed, parsed.Terms, true, len(cands), cands...)
	if !strings.Contains(plan, "TABLE_SCAN") || !strings.Contains(plan, "ilike_escape") {
		t.Fatal("plan missing TABLE_SCAN or substring filter")
	}
	if tableScanPushesILIKE(plan) {
		t.Fatal("ILIKE ran in TABLE_SCAN before candidate join")
	}
}

func TestSearchVerifyPlan70k(t *testing.T) {
	if os.Getenv("GMAIL_BENCH70K") == "" {
		t.Skip("set GMAIL_BENCH70K=1 to run the opt-in verify plan")
	}
	ctx := context.Background()
	path, reused, err := searchbench.MailboxPath()
	if err != nil {
		t.Fatal(err)
	}
	if !reused {
		t.Skip("GMAIL_BENCH_FIXTURE is required")
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ids := make([]string, 0, 140)
	for i := 0; i < 70000; i += 500 {
		ids = append(ids, fmt.Sprintf("id-%06d", i))
	}
	parsed, err := search.Parse(`"invoice leftover"`)
	if err != nil {
		t.Fatal(err)
	}
	f := ListFilter{Query: `"invoice leftover"`, Limit: 50}
	plan := explainList(t, db, f, parsed, parsed.Terms, true, len(ids), ids...)
	if !strings.Contains(plan, "TABLE_SCAN") || !strings.Contains(plan, "ilike_escape") {
		t.Fatalf("plan missing operators table_scan=%t ilike=%t plan_len=%d",
			strings.Contains(plan, "TABLE_SCAN"), strings.Contains(plan, "ilike_escape"), len(plan))
	}
	scanRows := tableScanRows(plan)
	t.Logf("verify_plan candidates=%d ilike_in_table_scan=%t table_scan_rows=%d streaming_limit=%t",
		len(ids), tableScanPushesILIKE(plan), scanRows, strings.Contains(plan, "STREAMING_LIMIT"))
	if tableScanPushesILIKE(plan) {
		t.Fatal("ILIKE ran in TABLE_SCAN before candidate join")
	}

	var times []time.Duration
	for i := 0; i < 5; i++ {
		t0 := time.Now()
		page, err := db.queryList(ctx, nil, f, parsed, parsed.Terms, true, len(ids), 0, false, time.Time{}, "", ids...)
		if err != nil {
			t.Fatal(err)
		}
		times = append(times, time.Since(t0))
		if i == 0 {
			t.Logf("verify_hits=%d", len(page))
		}
	}
	t.Logf("cached_verify_p50_ms=%.3f cached_verify_p95_ms=%.3f",
		searchbench.MS(searchbench.Median(times)), searchbench.MS(searchbench.P95(times)))
}

func newestFirst(ids []string) []string {
	out := append([]string{}, ids...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func explainList(t *testing.T, db *DB, f ListFilter, q search.Query, terms []string, useCached bool, limit int, ids ...string) string {
	t.Helper()
	query, args := buildListQuery(f, q, terms, useCached, limit, 0, false, time.Time{}, "", ids...)
	rows, err := db.sql.Query("EXPLAIN ANALYZE "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for rows.Next() {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for _, v := range raw {
			b.WriteString(fmt.Sprint(v))
			b.WriteByte('\n')
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func tableScanPushesILIKE(plan string) bool {
	for i := 0; i < len(plan); {
		j := strings.Index(plan[i:], "TABLE_SCAN")
		if j < 0 {
			return false
		}
		start := i + j
		end := start + 5000
		if end > len(plan) {
			end = len(plan)
		}
		if k := strings.Index(plan[start+10:], "TABLE_SCAN"); k >= 0 && start+10+k < end {
			end = start + 10 + k
		}
		if strings.Contains(plan[start:end], "ilike_escape") {
			return true
		}
		i = start + 10
	}
	return false
}

var scanRowsRe = regexp.MustCompile(`([0-9][0-9,]*)\s+rows`)

func tableScanRows(plan string) int {
	i := strings.Index(plan, "TABLE_SCAN")
	if i < 0 {
		return 0
	}
	chunk := plan[i:]
	if j := strings.Index(chunk, "Sequential Scan"); j >= 0 {
		chunk = chunk[j:]
	}
	if len(chunk) > 5000 {
		chunk = chunk[:5000]
	}
	best := 0
	for _, m := range scanRowsRe.FindAllStringSubmatch(chunk, 8) {
		n, err := strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
		if err == nil && n > best {
			best = n
		}
	}
	return best
}
