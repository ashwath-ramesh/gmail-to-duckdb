package query

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "mail.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestStatusEnvelope(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	env, err := Status(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if env.SchemaVersion != 1 {
		t.Fatalf("schema %d", env.SchemaVersion)
	}
	if env.BodyCoverage.SearchCovers != "metadata" {
		t.Fatalf("covers %s", env.BodyCoverage.SearchCovers)
	}
	if env.UntrustedContent {
		t.Fatal("status should not mark untrusted")
	}
	if env.Phase != "idle" {
		t.Fatalf("phase %s", env.Phase)
	}
}

func TestSearchAndGet(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []store.Message{{
		ID: "m1", ThreadID: "t1", InternalDate: at,
		FromEmail: "alice@x.com", Subject: "invoice 42", Snippet: "pay now",
		Body: "secret body", HasBody: true,
	}}); err != nil {
		t.Fatal(err)
	}
	env, err := Search(ctx, db, "from:alice invoice")
	if err != nil {
		t.Fatal(err)
	}
	if !env.UntrustedContent || len(env.UntrustedFields) == 0 || env.ResultCount != 1 || env.Messages[0].ID != "m1" {
		t.Fatalf("%+v", env)
	}
	if env.Messages[0].ThreadID != "t1" {
		t.Fatalf("thread %s", env.Messages[0].ThreadID)
	}
	if env.Messages[0].Body != "" {
		t.Fatal("search must omit body")
	}

	got, err := Get(ctx, db, "m1", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Message == nil || got.Message.Body != "" || !got.UntrustedContent {
		t.Fatalf("get %#v", got.Message)
	}
	full, err := Get(ctx, db, "m1", true)
	if err != nil {
		t.Fatal(err)
	}
	if full.Message.Body != "secret body" {
		t.Fatalf("body %q", full.Message.Body)
	}
}

func TestSchemaAndSQL(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	env, err := SchemaInfo(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if env.Schema == nil || env.Schema.Version != 1 || len(env.Schema.Tables) < 3 {
		t.Fatalf("%+v", env.Schema)
	}
	sqlEnv, err := SQL(ctx, db, "SELECT 1 AS n", false)
	if err != nil {
		t.Fatal(err)
	}
	if sqlEnv.SQL == nil || sqlEnv.ResultCount != 1 {
		t.Fatalf("%+v", sqlEnv.SQL)
	}
	if sqlEnv.UntrustedContent {
		t.Fatal("SELECT 1 must not mark untrusted")
	}
	if _, err := SQL(ctx, db, "DELETE FROM messages", false); err == nil {
		t.Fatal("expected write reject")
	}
}
