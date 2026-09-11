package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/searchbench"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/web"
)

func TestServeProcessToListening(t *testing.T) {
	if os.Getenv("GMAIL_BENCH_SERVE") == "" && os.Getenv("GMAIL_BENCH70K") == "" {
		t.Skip("set GMAIL_BENCH_SERVE=1 or GMAIL_BENCH70K=1")
	}
	if runtime.GOOS == "windows" && os.Getenv("GMAIL_BENCH_SERVE_FORCE") == "" {
		t.Skip("windows serve bench needs an explicit open stub")
	}
	bin := serveBenchBin(t)
	stubDir := t.TempDir()
	writeOpenStub(t, stubDir)
	emptyDir := t.TempDir()
	if err := os.Chmod(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	emptyDB := filepath.Join(emptyDir, "mail.duckdb")
	db, err := store.Open(emptyDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	emptyMS := launchUntilListening(t, bin, stubDir, emptyDB, false)
	t.Logf("empty_process_to_listening_ms=%.3f", searchbench.MS(emptyMS))

	path, ok, err := searchbench.MailboxPath()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Logf("fixture_absent skip populated launches")
		return
	}
	if runtime.GOOS != "linux" && os.Getenv("GMAIL_BENCH_SERVE_FORCE") == "" {
		t.Logf("populated_serve_skipped goos=%s", runtime.GOOS)
		return
	}
	const n = 10
	var durs []time.Duration
	for i := 0; i < n; i++ {
		if err := searchbench.RemoveSearchCache(path); err != nil {
			t.Fatal(err)
		}
		d := launchUntilListening(t, bin, stubDir, path, false)
		if d > 30*time.Second {
			t.Fatalf("process_to_listening_ms=%.3f exceeds 30s pathology bound", searchbench.MS(d))
		}
		durs = append(durs, d)
	}
	t.Logf("populated_missing_index_launches=%d process_to_listening_p50_ms=%.3f process_to_listening_p95_ms=%.3f",
		n, searchbench.MS(searchbench.Median(durs)), searchbench.MS(searchbench.P95(durs)))

	if err := searchbench.RemoveSearchCache(path); err != nil {
		t.Fatal(err)
	}
	overlapOK, overlapBuilding := launchListenOverlap(t, bin, stubDir, path)
	if overlapOK < 1 || overlapBuilding < 1 {
		t.Fatalf("startup overlap_ok=%d overlap_building=%d", overlapOK, overlapBuilding)
	}
	t.Logf("startup_overlap_probes=10 startup_overlap_ok=%d startup_overlap_building=%d", overlapOK, overlapBuilding)
}

func serveBenchBin(t *testing.T) string {
	t.Helper()
	if p := strings.TrimSpace(os.Getenv("GMAIL_BENCH_BIN")); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "gmail-to-duckdb")
	build := exec.Command("go", "build", "-o", bin, ".")
	if err := build.Run(); err != nil {
		t.Fatalf("build failed")
	}
	return bin
}

func writeOpenStub(t *testing.T, dir string) {
	t.Helper()
	names := []string{"xdg-open"}
	if runtime.GOOS == "darwin" {
		names = append(names, "open")
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func launchUntilListening(t *testing.T, bin, stubDir, dbPath string, keep bool) time.Duration {
	t.Helper()
	cmd, stdout := startServe(t, bin, stubDir, dbPath)
	t0 := time.Now()
	waitListening(t, stdout, 15*time.Second)
	d := time.Since(t0)
	if !keep {
		stopServe(t, cmd)
	}
	return d
}

func launchListenOverlap(t *testing.T, bin, stubDir, dbPath string) (int, int) {
	t.Helper()
	cmd, stdout := startServe(t, bin, stubDir, dbPath)
	defer stopServe(t, cmd)
	waitListening(t, stdout, 15*time.Second)
	deadline := time.Now().Add(2 * time.Minute)
	var info web.ServeInfo
	for {
		var err error
		info, err = web.ReadServeFile(dbPath)
		if err == nil && info.Token != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("serve file missing")
		}
		time.Sleep(20 * time.Millisecond)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	for {
		_, st := serveSQL(t, client, info)
		if st == store.IndexStateBuilding {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no building during startup")
		}
		time.Sleep(20 * time.Millisecond)
	}
	overlapOK := 0
	overlapBuilding := 0
	for i := 0; i < 10; i++ {
		code, st := serveSQL(t, client, info)
		if st == store.IndexStateBuilding {
			overlapBuilding++
		}
		if serveAPI(t, client, info, http.MethodGet, "/api/health", nil) == 200 && code == 200 {
			overlapOK++
		}
	}
	return overlapOK, overlapBuilding
}

func startServe(t *testing.T, bin, stubDir, dbPath string) (*exec.Cmd, io.Reader) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, bin, "serve", "--db", dbPath, "--credentials", filepath.Join(stubDir, "missing.json"), "--port", "0")
	cmd.Env = append(os.Environ(), "PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, stdout
}

func waitListening(t *testing.T, r io.Reader, d time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if strings.HasPrefix(strings.TrimSpace(sc.Text()), "listening on") {
				done <- nil
				return
			}
		}
		done <- sc.Err()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("no listening line: %v", err)
		}
	case <-time.After(d):
		t.Fatal("no listening line")
	}
}

func stopServe(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

func serveSQL(t *testing.T, c *http.Client, info web.ServeInfo) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(info.URL, "/")+"/api/sql", bytes.NewReader([]byte(`{"query":"SELECT count(*) FROM messages"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Token", info.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Do(req)
	if err != nil {
		return 0, ""
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		return res.StatusCode, ""
	}
	var env struct {
		SearchIndexState string `json:"search_index_state"`
		ResultCount      int    `json:"result_count"`
	}
	if err := json.Unmarshal(b, &env); err != nil || env.ResultCount < 1 {
		return res.StatusCode, ""
	}
	return res.StatusCode, env.SearchIndexState
}

func serveAPI(t *testing.T, c *http.Client, info web.ServeInfo, method, path string, body []byte) int {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, strings.TrimRight(info.URL, "/")+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Token", info.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.Do(req)
	if err != nil {
		return 0
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}
