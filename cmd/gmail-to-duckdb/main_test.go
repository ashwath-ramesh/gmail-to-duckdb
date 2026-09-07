package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

func seedDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "mail.duckdb")
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
	if env.SQL == nil || env.ResultCount != 1 || env.UntrustedContent {
		t.Fatalf("%+v", env)
	}
	out.Reset()
	if err := run([]string{"gmail-to-duckdb", "sql", "--db", dbPath, "DELETE FROM messages"}, strings.NewReader(""), &out, &out); err == nil {
		t.Fatal("expected write reject")
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
		if env.SchemaVersion != 1 {
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
