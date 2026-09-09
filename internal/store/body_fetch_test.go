package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
)

func TestUpdateBodyEmptyIsFetchedWithoutHasBody(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("m1", at)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBody(ctx, "m1", ""); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if got.HasBody {
		t.Fatalf("empty body must not set has_body: %+v", got)
	}
	if !got.BodyFetched {
		t.Fatalf("empty fetch must set body_fetched: %+v", got)
	}
	c, err := db.Coverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.WithBody != 0 || c.Total != 1 || c.SearchCovers() != "metadata" {
		t.Fatalf("coverage must count real bodies only: %+v", c)
	}
	ids, err := db.IDsNeedingFetch(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("completed empty must leave the fetch queue: %#v", ids)
	}
}

func TestUpsertMetadataPreservesFetchedAndBody(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 2, 15, 4, 5, 0, time.UTC)
	msg := sample("m1", at)
	msg.Body = "kept"
	msg.HasBody = true
	msg.BodyFetched = true
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	meta := sample("m1", at)
	meta.Subject = "later"
	if err := db.UpsertMessages(ctx, []Message{meta}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.BodyFetched || !got.HasBody || got.Body != "kept" {
		t.Fatalf("metadata refresh lost fetch state: %+v", got)
	}
	if got.Subject != "later" {
		t.Fatalf("subject: %q", got.Subject)
	}
}

func TestUpsertFullEmptyReplacesBody(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 2, 15, 4, 5, 0, time.UTC)
	msg := sample("m1", at)
	msg.Body = "old"
	msg.HasBody = true
	msg.BodyFetched = true
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	empty := sample("m1", at)
	empty.Body = ""
	empty.HasBody = false
	empty.BodyFetched = true
	if err := db.UpsertMessages(ctx, []Message{empty}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "" || got.HasBody || !got.BodyFetched {
		t.Fatalf("full empty must replace body: %+v", got)
	}
}

func TestIDsNeedingFetchKeyset(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("a", at), sample("b", at), sample("c", at)}); err != nil {
		t.Fatal(err)
	}
	first, err := db.IDsNeedingFetch(ctx, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0] != "a" || first[1] != "b" {
		t.Fatalf("first page: %#v", first)
	}
	next, err := db.IDsNeedingFetch(ctx, first[len(first)-1], 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0] != "c" {
		t.Fatalf("keyset must advance past last id: %#v", next)
	}
	if slices.Contains(next, first[0]) || slices.Contains(next, first[1]) {
		t.Fatalf("pages overlap: %#v %#v", first, next)
	}
}

func TestFreshOpenIsSchema2WithBodyFetched(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	v, ok, err := db.GetState(ctx, StateSchemaVersion)
	if err != nil || !ok || v != "2" {
		t.Fatalf("fresh schema version %q %v %v", v, ok, err)
	}
	tables, err := db.DescribeSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !schemaHasColumn(tables, "messages", "body_fetched") {
		t.Fatalf("fresh schema missing body_fetched: %#v", tables)
	}
}

func TestV1MigrationAndReopen(t *testing.T) {
	path := writeV1Fixture(t)
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	assertMigratedV1(t, db)

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = again.Close() })
	assertMigratedV1(t, again)
}

func TestRejectUnsupportedSchemaVersions(t *testing.T) {
	for _, ver := range []string{"-1", "99"} {
		t.Run(ver, func(t *testing.T) {
			path := writeV1Fixture(t)
			setRawState(t, path, StateSchemaVersion, ver)
			before := rawV1Snapshot(t, path)
			db, err := Open(path)
			if err == nil {
				_ = db.Close()
				t.Fatal("expected unsupported version reject")
			}
			if !strings.Contains(err.Error(), "unsupported schema_version") {
				t.Fatalf("err %v", err)
			}
			after := rawV1Snapshot(t, path)
			if after != before {
				t.Fatalf("body data changed: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestMigrateV2RollbackOnNormalizeFail(t *testing.T) {
	path := writeV1FixtureChecked(t)
	before := rawV1Snapshot(t, path)
	db, err := Open(path)
	if err == nil {
		_ = db.Close()
		t.Fatal("expected normalize failure")
	}
	after := rawV1Snapshot(t, path)
	if after != before {
		t.Fatalf("rollback lost v1 state: before=%+v after=%+v", before, after)
	}
	if after.Version != "1" || after.HasBodyFetched || !after.LegacyHasBody || after.PlainBody != "hello" {
		t.Fatalf("must remain v1: %+v", after)
	}
}

func assertMigratedV1(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	v, ok, err := db.GetState(ctx, StateSchemaVersion)
	if err != nil || !ok || v != "2" {
		t.Fatalf("migrated version %q %v %v", v, ok, err)
	}
	hid, ok, err := db.GetState(ctx, "history_id")
	if err != nil || !ok || hid != "42" {
		t.Fatalf("history lost: %q %v %v", hid, ok, err)
	}

	plain, err := db.GetMessage(ctx, "plain")
	if err != nil {
		t.Fatal(err)
	}
	if !plain.HasBody || !plain.BodyFetched || plain.Body != "hello" {
		t.Fatalf("plain: %+v", plain)
	}

	legacy, err := db.GetMessage(ctx, "empty-legacy")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.HasBody || !legacy.BodyFetched || legacy.Body != "" {
		t.Fatalf("legacy empty has_body must become fetched-only: %+v", legacy)
	}

	pending, err := db.GetMessage(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if pending.HasBody || pending.BodyFetched {
		t.Fatalf("pending: %+v", pending)
	}

	c, err := db.Coverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.Total != 3 || c.WithBody != 1 || c.SearchCovers() != "mixed" {
		t.Fatalf("coverage after migrate: %+v", c)
	}
}

func schemaHasColumn(tables []TableSchema, table, col string) bool {
	for _, t := range tables {
		if t.Name != table {
			continue
		}
		for _, c := range t.Columns {
			if c.Name == col {
				return true
			}
		}
	}
	return false
}

func writeV1FixtureChecked(t *testing.T) string {
	t.Helper()
	return writeV1DB(t, true)
}

func writeV1Fixture(t *testing.T) string {
	t.Helper()
	return writeV1DB(t, false)
}

func writeV1DB(t *testing.T, trapNormalize bool) string {
	t.Helper()
	path := filepath.Join(dbParent(t), "v1.duckdb")
	raw, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	check := ""
	if trapNormalize {
		check = ",\n  CHECK (id <> 'empty-legacy' OR has_body)"
	}
	_, err = raw.Exec(`
CREATE TABLE messages (
  id VARCHAR PRIMARY KEY,
  thread_id VARCHAR NOT NULL,
  history_id UBIGINT,
  internal_date TIMESTAMP NOT NULL,
  from_name VARCHAR,
  from_email VARCHAR,
  to_emails VARCHAR[],
  cc_emails VARCHAR[],
  subject VARCHAR,
  snippet VARCHAR,
  body VARCHAR,
  size_bytes INTEGER,
  label_ids VARCHAR[],
  is_read BOOLEAN,
  is_outgoing BOOLEAN,
  is_deleted BOOLEAN DEFAULT false,
  has_body BOOLEAN DEFAULT false,
  synced_at TIMESTAMP,
  search_text VARCHAR` + check + `
);
CREATE TABLE labels (
  id VARCHAR PRIMARY KEY,
  name VARCHAR,
  type VARCHAR
);
CREATE TABLE sync_state (
  key VARCHAR PRIMARY KEY,
  value VARCHAR
);
CREATE TABLE sync_seen (
  id VARCHAR PRIMARY KEY
);
INSERT INTO messages(id, thread_id, internal_date, from_name, from_email, to_emails, cc_emails, subject, snippet, body, size_bytes, label_ids, is_read, is_outgoing, is_deleted, has_body, synced_at)
VALUES
  ('plain', 't', TIMESTAMP '2024-01-01 00:00:00', '', 'a@x.com', [], [], 'plain', '', 'hello', 1, [], false, false, false, true, TIMESTAMP '2024-01-01 00:00:00'),
  ('empty-legacy', 't', TIMESTAMP '2024-01-01 00:00:00', '', 'a@x.com', [], [], 'empty', '', '', 1, [], false, false, false, true, TIMESTAMP '2024-01-01 00:00:00'),
  ('pending', 't', TIMESTAMP '2024-01-01 00:00:00', '', 'a@x.com', [], [], 'pending', '', NULL, 1, [], false, false, false, false, TIMESTAMP '2024-01-01 00:00:00');
INSERT INTO sync_state(key, value) VALUES
  ('schema_version', '1'),
  ('history_id', '42'),
  ('search_text_v1', '1');
`)
	closeErr := raw.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if err := privfile.Harden(path); err != nil {
		t.Fatal(err)
	}
	return path
}

type v1Snap struct {
	Version        string
	HasBodyFetched bool
	LegacyHasBody  bool
	PlainBody      string
	History        string
}

func setRawState(t *testing.T, path, key, value string) {
	t.Helper()
	raw, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`
INSERT INTO sync_state(key, value) VALUES (?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value
`, key, value)
	closeErr := raw.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func rawV1Snapshot(t *testing.T, path string) v1Snap {
	t.Helper()
	raw, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var s v1Snap
	if err := raw.QueryRow(`SELECT value FROM sync_state WHERE key = ?`, StateSchemaVersion).Scan(&s.Version); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT value FROM sync_state WHERE key = 'history_id'`).Scan(&s.History); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT has_body, COALESCE(body, '') FROM messages WHERE id = 'empty-legacy'`).Scan(&s.LegacyHasBody, new(string)); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT COALESCE(body, '') FROM messages WHERE id = 'plain'`).Scan(&s.PlainBody); err != nil {
		t.Fatal(err)
	}
	err = raw.QueryRow(`
SELECT count(*) > 0 FROM information_schema.columns
WHERE table_schema = 'main' AND table_name = 'messages' AND column_name = 'body_fetched'
`).Scan(&s.HasBodyFetched)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
