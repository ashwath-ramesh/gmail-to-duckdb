package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

func seedDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		dir = filepath.Join(dir, "db")
		if err := privfile.MkdirPrivate(dir); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "mail.duckdb")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessages(context.Background(), []store.Message{{
		ID: "m1", ThreadID: "t1", InternalDate: time.Unix(1, 0).UTC(),
		FromEmail: "alice@x.com", Subject: "invoice", Snippet: "pay",
		Body: "secret", HasBody: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func TestSQLCommand(t *testing.T) {
	dbPath := seedDB(t)
	var out bytes.Buffer
	if err := run([]string{"gmail-to-duckdb", "sql", "--db", dbPath, "SELECT id FROM messages"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "m1") {
		t.Fatalf("out %q", out.String())
	}
}

func TestSQLJSONAndReadOnly(t *testing.T) {
	dbPath := seedDB(t)
	var out bytes.Buffer
	if err := run([]string{"gmail-to-duckdb", "sql", "--db", dbPath, "--json", "SELECT 1 AS n"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	var env query.Envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.SQL == nil || env.ResultCount != 1 || !env.UntrustedContent {
		t.Fatalf("%+v", env)
	}
	if len(env.UntrustedFields) != 1 || env.UntrustedFields[0] != "n" {
		t.Fatalf("fields %#v", env.UntrustedFields)
	}
	if fmt.Sprint(env.SQL.Rows[0][0]) != "1" {
		t.Fatalf("payload %#v", env.SQL.Rows)
	}
	out.Reset()
	if err := run([]string{"gmail-to-duckdb", "sql", "--db", dbPath, "DELETE FROM messages"}, strings.NewReader(""), &out, &out); err == nil {
		t.Fatal("expected write reject")
	}
}

func TestSQLExploitDoesNotDelete(t *testing.T) {
	dbPath := seedDB(t)
	var out bytes.Buffer
	if err := run([]string{"gmail-to-duckdb", "sql", "--db", dbPath, `SELECT 1 AS "--"; DELETE FROM messages`}, strings.NewReader(""), &out, &out); err == nil {
		t.Fatal("expected exploit reject")
	}
	out.Reset()
	if err := run([]string{"gmail-to-duckdb", "sql", "--db", dbPath, "SELECT id FROM messages"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "m1") {
		t.Fatalf("data lost: %s", out.String())
	}
}

func TestSQLStdin(t *testing.T) {
	dbPath := seedDB(t)
	var out bytes.Buffer
	if err := run([]string{"gmail-to-duckdb", "sql", "--db", dbPath, "--json"}, strings.NewReader("SELECT id FROM messages"), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "m1") {
		t.Fatalf("%s", out.String())
	}
}

func TestStatusSearchGetSchema(t *testing.T) {
	dbPath := seedDB(t)
	for _, args := range [][]string{
		{"gmail-to-duckdb", "status", "--db", dbPath, "--json"},
		{"gmail-to-duckdb", "search", "--db", dbPath, "--json", "from:alice invoice"},
		{"gmail-to-duckdb", "get", "--db", dbPath, "--json", "m1"},
		{"gmail-to-duckdb", "schema", "--db", dbPath, "--json"},
	} {
		var out bytes.Buffer
		if err := run(args, strings.NewReader(""), &out, &out); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out.String())
		}
		var env query.Envelope
		if err := json.Unmarshal(out.Bytes(), &env); err != nil {
			t.Fatalf("%v: %v %s", args, err, out.String())
		}
		if env.SchemaVersion != 3 {
			t.Fatalf("%v schema %d", args, env.SchemaVersion)
		}
	}
	var out bytes.Buffer
	if err := run([]string{"gmail-to-duckdb", "get", "--db", dbPath, "--json", "m1"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	var env query.Envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Message == nil || env.Message.Body != "" || env.Message.ThreadID != "t1" {
		t.Fatalf("get without body %#v", env.Message)
	}
	out.Reset()
	if err := run([]string{"gmail-to-duckdb", "get", "--db", dbPath, "--json", "--body", "m1"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Message.Body != "secret" || !env.UntrustedContent {
		t.Fatalf("get --body %#v", env.Message)
	}
}

func TestSearchJSONAfterQuery(t *testing.T) {
	dbPath := seedDB(t)
	var out bytes.Buffer
	if err := run([]string{"gmail-to-duckdb", "search", "--db", dbPath, "invoice", "--json"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	var env query.Envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("%v %s", err, out.String())
	}
	if env.ResultCount < 1 || len(env.Messages) == 0 {
		t.Fatalf("%s", out.String())
	}
}

func TestHelp(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"gmail-to-duckdb", "help"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "sync") || !strings.Contains(out.String(), "serve") {
		t.Fatalf("%q", out.String())
	}
}

func TestDoctorJSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out bytes.Buffer
	err := run([]string{"gmail-to-duckdb", "doctor", "--json", "--db", filepath.Join(t.TempDir(), "missing.duckdb"), "--credentials", filepath.Join(t.TempDir(), "no.json")}, strings.NewReader(""), &out, &out)
	if err == nil {
		t.Fatal("expected doctor failure")
	}
	var env query.Envelope
	if e := json.Unmarshal(out.Bytes(), &env); e != nil {
		t.Fatal(e)
	}
	if len(env.Checks) == 0 {
		t.Fatalf("%s", out.String())
	}
}
