package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/config"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
)

func TestSearchRejectsInvalidQuery(t *testing.T) {
	dbPath := seedDB(t)
	var out bytes.Buffer
	err := run([]string{"gmail-to-duckdb", "search", "--db", dbPath, "after:nope"}, strings.NewReader(""), &out, &out)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "after") {
		t.Fatalf("msg %v", err)
	}
}

func TestSearchJoinsUnquotedWords(t *testing.T) {
	dbPath := seedDB(t)
	var out bytes.Buffer
	// "invoice" matches m1, "nomatchword" matches nothing: AND of both is empty.
	if err := run([]string{"gmail-to-duckdb", "search", "--db", dbPath, "--json", "invoice", "nomatchword"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	var env query.Envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.ResultCount != 0 {
		t.Fatalf("second word ignored: %s", out.String())
	}
}

func TestInitPrintsServeAsNextStep(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cred := filepath.Join(t.TempDir(), "client_secret.json")
	if err := os.WriteFile(cred, []byte(`{"installed":{"client_id":"x.apps.googleusercontent.com","client_secret":"y"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	var out bytes.Buffer
	if err := run([]string{"gmail-to-duckdb", "init", "--credentials", cred, "--db", "rel/mail.duckdb"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatalf("%v %s", err, out.String())
	}
	if strings.Contains(out.String(), "doctor &&") || !strings.Contains(out.String(), "next: gmail-to-duckdb serve") {
		t.Fatalf("next step: %s", out.String())
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(cfg.DB) {
		t.Fatalf("config keeps a relative db path: %q", cfg.DB)
	}
}

func TestSQLWarnsWhenResultIsTruncated(t *testing.T) {
	dbPath := seedDB(t)
	var out, errOut bytes.Buffer
	if err := run([]string{"gmail-to-duckdb", "sql", "--db", dbPath, "--json", "SELECT i FROM range(200000) t(i)"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var env query.Envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if !env.Truncated || env.ResultCount != 100000 {
		t.Fatalf("truncated=%v count=%d", env.Truncated, env.ResultCount)
	}
	if !strings.Contains(errOut.String(), "showing the first 100000 rows") {
		t.Fatalf("stderr %q", errOut.String())
	}
}
