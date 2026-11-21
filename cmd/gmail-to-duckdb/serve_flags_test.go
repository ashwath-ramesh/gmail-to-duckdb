package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/web"
)

func TestSyncIntervalMinimum(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 0, "0": 0, "1m": time.Minute, "5m": 5 * time.Minute} {
		got, err := syncInterval(in)
		if err != nil || got != want {
			t.Fatalf("%q: got %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"1s", "59s", "-5m", "soon"} {
		if _, err := syncInterval(in); err == nil {
			t.Fatalf("%q: want an error", in)
		}
	}
}

func TestOpenUIWithoutBrowserOnlyPrints(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mail.duckdb")
	var out, errOut bytes.Buffer
	err := openUI(&out, &errOut, dbPath, false, func() (string, error) {
		return "http://127.0.0.1:8080/#code=abc", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "#code=abc") {
		t.Fatalf("stdout %q", out.String())
	}
	if _, err := os.Stat(web.LauncherPath(dbPath)); !os.IsNotExist(err) {
		t.Fatalf("launcher written without a browser: %v", err)
	}
}
