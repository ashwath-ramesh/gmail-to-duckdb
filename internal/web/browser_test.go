package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/searchbench"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

const (
	browserAlpha = 110
	browserBeta  = 20
)

func TestBrowserSearchHarness(t *testing.T) {
	if goruntime.GOOS != "linux" && os.Getenv("GMAIL_PLAYWRIGHT_FORCE") == "" {
		t.Skip("linux-only opt-in browser harness")
	}
	chrome := os.Getenv("GMAIL_PLAYWRIGHT_CHROME")
	pkg := os.Getenv("GMAIL_PLAYWRIGHT_PKG")
	if chrome == "" || pkg == "" {
		t.Skip("set GMAIL_PLAYWRIGHT_CHROME and GMAIL_PLAYWRIGHT_PKG")
	}
	if _, err := os.Stat(chrome); err != nil {
		t.Skip("chromium executable missing")
	}
	if _, err := os.Stat(pkg); err != nil {
		t.Skip("playwright package missing")
	}
	ctx := context.Background()
	s, db := browserServer(t)
	unreadBeta, err := ensureBrowserMarkers(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	maintCtx, maintCancel := context.WithCancel(ctx)
	defer maintCancel()
	db.StartMaintenance(maintCtx)
	if err := waitIndexReady(ctx, db, 8*time.Minute); err != nil {
		t.Fatal(err)
	}
	seedStatus(t, s)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenURL, h, err := s.BindListener(ln)
	if err != nil {
		t.Fatal(err)
	}
	srvCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- Serve(srvCtx, ln, h) }()
	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	script := filepath.Join(filepath.Dir(file), "testdata", "browser-harness.js")
	cmd := exec.Command("node", script, listenURL, chrome, pkg,
		strconv.Itoa(browserAlpha), strconv.Itoa(browserBeta), strconv.Itoa(unreadBeta))
	cmd.Env = append(os.Environ(), "PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		if msg == "" {
			msg = err.Error()
		}
		t.Fatalf("harness failed: %s", msg)
	}
	var report struct {
		Counts       map[string]int `json:"counts"`
		SearchP50MS  float64        `json:"search_p50_ms"`
		SearchP95MS  float64        `json:"search_p95_ms"`
		SearchRuns   int            `json:"search_runs"`
		EnterDelayMS float64        `json:"enter_delay_ms"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.SearchRuns < 10 {
		t.Fatalf("search_runs=%d", report.SearchRuns)
	}
	if report.SearchP50MS < 100 {
		t.Fatalf("search_p50_ms=%.3f below debounce", report.SearchP50MS)
	}
	if report.Counts["rows_final"] != browserAlpha || report.Counts["rows_unread"] != unreadBeta || report.Counts["rows_after_stale"] != 50 || report.Counts["rows_after_error"] != 50 || report.Counts["enter_requests"] != 1 {
		t.Fatalf("counts %#v", report.Counts)
	}
	t.Logf("browser search_runs=%d search_p50_ms=%.3f search_p95_ms=%.3f enter_delay_ms=%.3f rows_final=%d rows_unread=%d",
		report.SearchRuns, report.SearchP50MS, report.SearchP95MS, report.EnterDelayMS, report.Counts["rows_final"], report.Counts["rows_unread"])
	cancel()
	<-errc
}

func browserServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	path, ok, err := searchbench.MailboxPath()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		dir := t.TempDir()
		if goruntime.GOOS != "windows" {
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		path = filepath.Join(dir, "mail.duckdb")
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Server{DB: db, Token: "bench"}, db
}

func ensureBrowserMarkers(ctx context.Context, db *store.DB) (int, error) {
	alpha, err := searchbench.MarkerCount(ctx, db.SQL(), "alphatoken")
	if err != nil {
		return 0, err
	}
	beta, err := searchbench.MarkerCount(ctx, db.SQL(), "betatoken")
	if err != nil {
		return 0, err
	}
	if alpha < browserAlpha || beta < browserBeta {
		rev, err := searchbench.CorpusRev(ctx, db.SQL())
		if err != nil {
			return 0, err
		}
		next := int(rev + 1)
		if err := searchbench.SeedMarkers(ctx, db.SQL(), "alphatoken", browserAlpha, next); err != nil {
			return 0, err
		}
		if err := searchbench.SeedMarkers(ctx, db.SQL(), "betatoken", browserBeta, next); err != nil {
			return 0, err
		}
		if err := db.SetState(ctx, "search_corpus_revision", strconv.Itoa(next)); err != nil {
			return 0, err
		}
	}
	var unread int
	err = db.SQL().QueryRowContext(ctx, `
SELECT COUNT(*) FROM messages
WHERE COALESCE(search_text, '') ILIKE ?
  AND NOT is_read AND NOT is_deleted`, "%betatoken%").Scan(&unread)
	if err != nil {
		return 0, err
	}
	if unread < 1 {
		return 0, errors.New("unread markers")
	}
	return unread, nil
}

func waitIndexReady(ctx context.Context, db *store.DB, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		ok, err := db.HasFTS(ctx)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return errIndexWait{}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

type errIndexWait struct{}

func (errIndexWait) Error() string { return "index not ready" }
