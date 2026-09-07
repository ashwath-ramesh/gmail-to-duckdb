package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

func TestSQLCommand(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mail.duckdb")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessages(context.Background(), []store.Message{{
		ID: "m1", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(),
		FromEmail: "a@x.com", Subject: "Hi",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"gmail-to-duckdb", "sql", "--db", dbPath, "SELECT id FROM messages"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "m1") {
		t.Fatalf("out %q", out.String())
	}
}

func TestHelp(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"gmail-to-duckdb", "help"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "sync") {
		t.Fatalf("%q", out.String())
	}
}
