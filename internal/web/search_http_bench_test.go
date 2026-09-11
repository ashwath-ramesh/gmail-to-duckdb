package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/searchbench"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

func TestSearchHTTPBenchmark70k(t *testing.T) {
	if os.Getenv("GMAIL_BENCH70K") == "" {
		t.Skip("set GMAIL_BENCH70K=1 to run the opt-in 70k HTTP bench")
	}
	ctx := context.Background()
	path, ok, err := searchbench.MailboxPath()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path = filepath.Join(dir, "bench.duckdb")
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	n, _, err := searchbench.NextIDRange(ctx, db.SQL(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		if err := searchbench.SeedRange(ctx, db.SQL(), 0, 70000, 1); err != nil {
			t.Fatal(err)
		}
		if err := db.SetState(ctx, "search_corpus_revision", "1"); err != nil {
			t.Fatal(err)
		}
	}
	ready, err := db.HasFTS(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		if err := db.RebuildFTS(ctx); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{DB: db, Token: "bench"}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base, h, err := s.BindListener(ln)
	if err != nil {
		t.Fatal(err)
	}
	srvCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- Serve(srvCtx, ln, h) }()
	client := &http.Client{Timeout: 60 * time.Second}
	want, err := tableCount(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	const warm = 10
	search := timeLoopback(t, client, base, http.MethodGet, "/api/messages?q=sharedword", nil, warm, want)
	health := timeLoopback(t, client, base, http.MethodGet, "/api/health", nil, warm, want)
	count := timeLoopback(t, client, base, http.MethodPost, "/api/sql", []byte(`{"query":"SELECT count(*) FROM messages"}`), warm, want)

	start, end, err := searchbench.NextIDRange(ctx, db.SQL(), 250)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := searchbench.CorpusRev(ctx, db.SQL())
	if err != nil {
		t.Fatal(err)
	}
	incStart := time.Now()
	if err := searchbench.SeedRange(ctx, db.SQL(), start, end, int(rev+1)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, "search_corpus_revision", strconv.FormatInt(rev+1, 10)); err != nil {
		t.Fatal(err)
	}
	st, err := db.SearchIndexState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st == store.IndexStateBuilding {
		t.Fatal("incremental pending must not report building")
	}
	maintCtx, maintCancel := context.WithCancel(ctx)
	defer maintCancel()
	db.StartMaintenance(maintCtx)
	if err := waitIndexState(ctx, db, store.IndexStateReady, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	incDur := time.Since(incStart)
	if st, err := db.SearchIndexState(ctx); err != nil || st == store.IndexStateBuilding {
		t.Fatalf("delta ended in %s %v", st, err)
	}
	want, err = tableCount(ctx, db)
	if err != nil {
		t.Fatal(err)
	}

	var searchD, healthD, countD []time.Duration
	healthOverlap := 0
	countOverlap := 0
	for i := 0; i < warm; i++ {
		type fin struct {
			end time.Time
			err error
		}
		startedCh := make(chan time.Time, 1)
		done := make(chan fin, 1)
		go func() {
			started := time.Now()
			startedCh <- started
			_, err := loopback(t, client, base, http.MethodGet, "/api/messages?q=sharedword", nil, want)
			done <- fin{time.Now(), err}
		}()
		started := <-startedCh
		h0 := time.Now()
		if _, err := loopback(t, client, base, http.MethodGet, "/api/health", nil, want); err != nil {
			t.Fatal(err)
		}
		h1 := time.Now()
		c0 := time.Now()
		if _, err := loopback(t, client, base, http.MethodPost, "/api/sql", []byte(`{"query":"SELECT count(*) FROM messages"}`), want); err != nil {
			t.Fatal(err)
		}
		c1 := time.Now()
		got := <-done
		if got.err != nil {
			t.Fatal(got.err)
		}
		searchD = append(searchD, got.end.Sub(started))
		healthD = append(healthD, h1.Sub(h0))
		countD = append(countD, c1.Sub(c0))
		if overlapWindow(started, got.end, h0, h1) {
			healthOverlap++
		}
		if overlapWindow(started, got.end, c0, c1) {
			countOverlap++
		}
	}

	rebuildErr := make(chan error, 1)
	go func() { rebuildErr <- db.RebuildFTS(ctx) }()
	if err := waitIndexState(ctx, db, store.IndexStateBuilding, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	var rebuildHealth, rebuildCount []time.Duration
	rebuildOverlap := 0
	rebuildOK := 0
	for i := 0; i < warm; i++ {
		st, err := db.SearchIndexState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t0 := time.Now()
		if _, err := loopback(t, client, base, http.MethodGet, "/api/health", nil, want); err != nil {
			t.Fatal(err)
		}
		rebuildHealth = append(rebuildHealth, time.Since(t0))
		t1 := time.Now()
		if _, err := loopback(t, client, base, http.MethodPost, "/api/sql", []byte(`{"query":"SELECT count(*) FROM messages"}`), want); err != nil {
			t.Fatal(err)
		}
		rebuildCount = append(rebuildCount, time.Since(t1))
		rebuildOK++
		if st == store.IndexStateBuilding {
			rebuildOverlap++
		}
	}
	if err := <-rebuildErr; err != nil {
		t.Fatal(err)
	}
	if rebuildOK != warm {
		t.Fatalf("rebuild probes %d", rebuildOK)
	}

	t.Logf("http_loopback_search_p50_ms=%.3f http_loopback_search_p95_ms=%.3f http_loopback_health_p50_ms=%.3f http_loopback_health_p95_ms=%.3f http_loopback_count_p50_ms=%.3f http_loopback_count_p95_ms=%.3f",
		searchbench.MS(searchbench.Median(search)), searchbench.MS(searchbench.P95(search)),
		searchbench.MS(searchbench.Median(health)), searchbench.MS(searchbench.P95(health)),
		searchbench.MS(searchbench.Median(count)), searchbench.MS(searchbench.P95(count)))
	t.Logf("search_inflight_probes=%d search_health_overlap=%d search_count_overlap=%d search_inflight_p95_ms=%.3f",
		warm, healthOverlap, countOverlap, searchbench.MS(searchbench.P95(searchD)))
	t.Logf("incremental_apply_ms=%.3f incremental_state=pending_then_ready", searchbench.MS(incDur))
	t.Logf("rebuild_probes=%d rebuild_ok=%d rebuild_building_overlap=%d rebuild_health_p95_ms=%.3f rebuild_count_p95_ms=%.3f",
		warm, rebuildOK, rebuildOverlap, searchbench.MS(searchbench.P95(rebuildHealth)), searchbench.MS(searchbench.P95(rebuildCount)))
	t.Logf("search_inflight_health_p95_ms=%.3f search_inflight_count_p95_ms=%.3f",
		searchbench.MS(searchbench.P95(healthD)), searchbench.MS(searchbench.P95(countD)))
	t.Logf("http_transport=loopback_socket")
	cancel()
	<-errc
}

func tableCount(ctx context.Context, db *store.DB) (int64, error) {
	var n int64
	err := db.SQL().QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&n)
	return n, err
}

func waitIndexState(ctx context.Context, db *store.DB, want string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		st, err := db.SearchIndexState(ctx)
		if err != nil {
			return err
		}
		if st == want {
			return nil
		}
		if time.Now().After(deadline) {
			return errIndexWait{}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func overlapWindow(a0, a1, b0, b1 time.Time) bool {
	return b0.Before(a1) && b1.After(a0)
}

func timeLoopback(t *testing.T, c *http.Client, base, method, path string, body []byte, n int, want int64) []time.Duration {
	t.Helper()
	var out []time.Duration
	for i := 0; i < n; i++ {
		t0 := time.Now()
		if _, err := loopback(t, c, base, method, path, body, want); err != nil {
			t.Fatal(err)
		}
		out = append(out, time.Since(t0))
	}
	return out
}

func loopback(t *testing.T, c *http.Client, base, method, path string, body []byte, want int64) (int64, error) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Token", "bench")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, err
	}
	if res.StatusCode != 200 {
		return 0, errHTTPStatus{res.StatusCode}
	}
	if path == "/api/health" {
		return 0, nil
	}
	var env struct {
		ResultCount int `json:"result_count"`
		SQL         *struct {
			Rows [][]any `json:"rows"`
		} `json:"sql"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return 0, err
	}
	if path == "/api/sql" {
		if env.SQL == nil || len(env.SQL.Rows) < 1 || len(env.SQL.Rows[0]) < 1 {
			return 0, errCountEmpty{}
		}
		n, ok := jsonInt(env.SQL.Rows[0][0])
		if !ok || n != want {
			return n, errCountValue{n, want}
		}
		return n, nil
	}
	if env.ResultCount < 1 {
		return 0, errCountEmpty{}
	}
	return int64(env.ResultCount), nil
}

func jsonInt(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), float64(int64(n)) == n
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case int64:
		return n, true
	case int:
		return int64(n), true
	default:
		return 0, false
	}
}

type errHTTPStatus struct{ code int }

func (e errHTTPStatus) Error() string { return "http status" }

type errCountEmpty struct{}

func (errCountEmpty) Error() string { return "count result empty" }

type errCountValue struct{ got, want int64 }

func (e errCountValue) Error() string { return "count value" }
