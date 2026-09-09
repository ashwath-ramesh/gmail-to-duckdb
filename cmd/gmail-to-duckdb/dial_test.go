package main

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	ln, err := web.Listen("0")
	if err != nil {
		t.Fatal(err)
	}
	s := &web.Server{DB: db, Token: "tok"}
	u, h, err := s.BindListener(ln)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(h)
	_ = ts.Listener.Close()
	ts.Listener = ln
	ts.Start()
	t.Cleanup(ts.Close)
	if err := web.WriteServeFile(dbPath, web.ServeInfo{URL: u, Token: "tok", PID: 1}); err != nil {
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
	if env.UntrustedContent {
		t.Fatal("remote status must stay trusted")
	}
	out.Reset()
	if err := run([]string{"gmail-to-duckdb", "schema", "--db", dbPath, "--json"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.UntrustedContent {
		t.Fatal("remote schema must stay trusted")
	}
	out.Reset()
	if err := run([]string{"gmail-to-duckdb", "sql", "--db", dbPath, "--json", "SELECT 1 AS n"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.SQL == nil || env.ResultCount != 1 || !env.UntrustedContent {
		t.Fatalf("%s", out.String())
	}
	if len(env.UntrustedFields) != 1 || env.UntrustedFields[0] != "n" {
		t.Fatalf("fields %#v", env.UntrustedFields)
	}
	if fmt.Sprint(env.SQL.Rows[0][0]) != "1" {
		t.Fatalf("payload %#v", env.SQL.Rows)
	}
	out.Reset()
	if err := run([]string{"gmail-to-duckdb", "sql", "--db", dbPath, "--json", "SELECT row_to_json(messages) AS data FROM messages"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.SQL == nil || !env.UntrustedContent || len(env.UntrustedFields) != 1 || env.UntrustedFields[0] != "data" {
		t.Fatalf("%s", out.String())
	}
	raw, err := json.Marshal(env.SQL.Rows)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "invoice") || !strings.Contains(string(raw), "secret") {
		t.Fatalf("payload %s", raw)
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

func TestCLIRefuseUnreadableServe(t *testing.T) {
	dbPath := seedDB(t)
	if err := os.Mkdir(web.ServePath(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := run([]string{"gmail-to-duckdb", "status", "--db", dbPath, "--json"}, strings.NewReader(""), &out, &out)
	if err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("err %v out %s", err, out.String())
	}
}
