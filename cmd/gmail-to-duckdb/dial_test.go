package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/web"
)

func TestCLIDialsServe(t *testing.T) {
	dbPath := seedDB(t)
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := &web.Server{DB: db, Token: "tok"}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	if err := web.WriteServeFile(dbPath, web.ServeInfo{URL: ts.URL, Token: "tok", PID: 1}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"gmail-to-duckdb", "status", "--db", dbPath, "--json"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	var env query.Envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.SchemaVersion != 1 {
		t.Fatalf("%s", out.String())
	}
}

func TestCLIRefuseStaleServe(t *testing.T) {
	dbPath := seedDB(t)
	if err := os.WriteFile(web.ServePath(dbPath), []byte(`{"url":"http://127.0.0.1:1","token":"x","pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := run([]string{"gmail-to-duckdb", "status", "--db", dbPath, "--json"}, strings.NewReader(""), &out, &out)
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("err %v out %s", err, out.String())
	}
}
