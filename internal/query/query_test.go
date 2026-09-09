package query

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

func testDB(t *testing.T) *store.DB {
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
	db, err := store.Open(filepath.Join(dir, "mail.duckdb"))
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
	if env.UntrustedContent {
		t.Fatal("schema should not mark untrusted")
	}
	sqlEnv, err := SQL(ctx, db, "SELECT 1 AS n", false)
	if err != nil {
		t.Fatal(err)
	}
	if sqlEnv.SQL == nil || sqlEnv.ResultCount != 1 {
		t.Fatalf("%+v", sqlEnv.SQL)
	}
	assertSQLUntrusted(t, sqlEnv, []string{"n"}, 1)
	if fmt.Sprint(sqlEnv.SQL.Rows[0][0]) != "1" {
		t.Fatalf("payload %#v", sqlEnv.SQL.Rows)
	}
	if _, err := SQL(ctx, db, "DELETE FROM messages", false); err == nil {
		t.Fatal("expected write reject")
	}
}

func TestSQLUntrustedMarkers(t *testing.T) {
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

	view, err := SQL(ctx, db, "CREATE VIEW mail_titles AS SELECT upper(subject) AS t FROM messages", true)
	if err != nil {
		t.Fatal(err)
	}
	assertSQLUntrusted(t, view, view.SQL.Columns, view.ResultCount)

	cases := []struct {
		name string
		q    string
		cols []string
		rows int
		want []string
	}{
		{"select1", "SELECT 1 AS n", []string{"n"}, 1, []string{"1"}},
		{"empty", "SELECT 1 AS n FROM messages WHERE id = 'missing'", []string{"n"}, 0, nil},
		{"renamed", "SELECT subject AS title FROM messages", []string{"title"}, 1, []string{"invoice 42"}},
		{"fields", "SELECT subject, snippet FROM messages", []string{"subject", "snippet"}, 1, []string{"invoice 42", "pay now"}},
		{"cte", "WITH x AS (SELECT upper(subject) AS t FROM messages) SELECT t FROM x", []string{"t"}, 1, []string{"INVOICE 42"}},
		{"view", "SELECT t FROM mail_titles", []string{"t"}, 1, []string{"INVOICE 42"}},
		{"agg", "SELECT count(*) AS n, max(subject) AS title FROM messages", []string{"n", "title"}, 1, []string{"1", "invoice 42"}},
		{"rowjson", "SELECT row_to_json(messages) AS data FROM messages", []string{"data"}, 1, []string{"invoice 42", "secret body"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := SQL(ctx, db, tc.q, false)
			if err != nil {
				t.Fatal(err)
			}
			assertSQLUntrusted(t, env, tc.cols, tc.rows)
			if tc.rows == 0 {
				return
			}
			payload := mustJSON(t, env.SQL.Rows)
			for _, w := range tc.want {
				if !strings.Contains(payload, w) {
					t.Fatalf("payload %s missing %q", payload, w)
				}
			}
		})
	}

	ins, err := SQL(ctx, db, "INSERT INTO labels(id, name, type) VALUES ('x', 'X', 'user')", true)
	if err != nil {
		t.Fatal(err)
	}
	assertSQLUntrusted(t, ins, ins.SQL.Columns, ins.ResultCount)
	if ins.ResultCount != 1 || !strings.Contains(mustJSON(t, ins.SQL.Rows), "1") {
		t.Fatalf("write payload %#v", ins.SQL)
	}
}

func assertSQLUntrusted(t *testing.T, env Envelope, cols []string, rows int) {
	t.Helper()
	if env.SQL == nil {
		t.Fatal("missing sql payload")
	}
	if !env.UntrustedContent {
		t.Fatal("sql envelope must mark untrusted")
	}
	if !slices.Equal(env.SQL.Columns, cols) {
		t.Fatalf("columns %#v want %#v", env.SQL.Columns, cols)
	}
	if !slices.Equal(env.UntrustedFields, cols) {
		t.Fatalf("untrusted_fields %#v want %#v", env.UntrustedFields, cols)
	}
	if env.ResultCount != rows || len(env.SQL.Rows) != rows {
		t.Fatalf("rows %d count %d want %d", len(env.SQL.Rows), env.ResultCount, rows)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
