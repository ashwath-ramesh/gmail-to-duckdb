package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
)

func TestSchemaVersionIs4(t *testing.T) {
	if SchemaVersion != 4 {
		t.Fatalf("SchemaVersion=%d want 4", SchemaVersion)
	}
	ctx := context.Background()
	db := testDB(t)
	v, ok, err := db.GetState(ctx, StateSchemaVersion)
	if err != nil || !ok || v != "4" {
		t.Fatalf("fresh schema version %q %v %v", v, ok, err)
	}
}

func TestBodyUpdateMarksFTSDirty(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	msg := sample("m1", at)
	msg.Subject = "pineapple note"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	ok, err := db.HasFTS(ctx)
	if err != nil || !ok {
		t.Fatalf("ready after rebuild %v %v", ok, err)
	}
	if err := db.UpdateBody(ctx, "m1", "unique body text"); err != nil {
		t.Fatal(err)
	}
	ok, err = db.HasFTS(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("dirty index after body update currently reports ready")
	}
}

func TestListRoundtripControlChars(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	msg := sample("esc", at)
	msg.ToEmails = []string{
		"user\r@cr.com",
		"a\tb",
		"c\nd",
		"e\v f",
		"g\fh",
		`quote"slash\`,
		"unicodé",
	}
	msg.CcEmails = []string{"cc\r@x.com", "cc\t@y.com"}
	msg.LabelIDs = []string{"INBOX", "tag\r\n", `lab"el\`}
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "esc")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.ToEmails, msg.ToEmails) {
		t.Fatalf("to roundtrip corrupted:\n got %#v\nwant %#v", got.ToEmails, msg.ToEmails)
	}
	if !slices.Equal(got.CcEmails, msg.CcEmails) {
		t.Fatalf("cc roundtrip corrupted:\n got %#v\nwant %#v", got.CcEmails, msg.CcEmails)
	}
	if !slices.Equal(got.LabelIDs, msg.LabelIDs) {
		t.Fatalf("labels roundtrip corrupted:\n got %#v\nwant %#v", got.LabelIDs, msg.LabelIDs)
	}
	if err := db.UpdateLabels(ctx, "esc", msg.LabelIDs, true); err != nil {
		t.Fatal(err)
	}
	again, err := db.GetMessage(ctx, "esc")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(again.LabelIDs, msg.LabelIDs) {
		t.Fatalf("UpdateLabels roundtrip corrupted:\n got %#v\nwant %#v", again.LabelIDs, msg.LabelIDs)
	}
}

func TestHeadersNullEmptyAndSQL(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	old := sample("old", at)
	if err := db.UpsertMessages(ctx, []Message{old}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "old")
	if err != nil {
		t.Fatal(err)
	}
	if got.Headers != nil {
		t.Fatalf("uncollected must be nil: %#v", got.Headers)
	}
	empty := sample("empty", at)
	empty.Headers = []Header{}
	dups := sample("dups", at)
	dups.Headers = []Header{{Name: "Received", Value: "a"}, {Name: "received", Value: "b"}}
	if err := db.UpsertMessages(ctx, []Message{empty, dups}); err != nil {
		t.Fatal(err)
	}
	eg, err := db.GetMessage(ctx, "empty")
	if err != nil {
		t.Fatal(err)
	}
	if eg.Headers == nil || len(eg.Headers) != 0 {
		t.Fatalf("empty collected %#v", eg.Headers)
	}
	dg, err := db.GetMessage(ctx, "dups")
	if err != nil {
		t.Fatal(err)
	}
	if len(dg.Headers) != 2 || dg.Headers[0].Name != "Received" || dg.Headers[1].Name != "received" {
		t.Fatalf("dups %#v", dg.Headers)
	}
	res, err := db.QuerySQL(ctx, "SELECT id, headers FROM messages WHERE id IN ('old','empty','dups') ORDER BY id", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("sql rows %#v", res.Rows)
	}
	if res.Rows[2][1] != nil {
		t.Fatalf("old sql headers %#v", res.Rows[2][1])
	}
	tables, err := db.DescribeSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !schemaHasColumn(tables, "messages", "headers") {
		t.Fatal("schema missing headers")
	}
}

func TestBodyUpdateDoesNotOverwriteMetadataOrNewerBody(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	msg := sample("m1", at)
	msg.Subject = "live"
	msg.LabelIDs = []string{"INBOX"}
	msg.Headers = []Header{{Name: "Subject", Value: "live"}}
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyBodyUpdates(ctx, []BodyUpdate{{
		ID: "m1", Body: "first", Headers: []Header{{Name: "Subject", Value: "stale"}},
	}}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "live" || got.Body != "first" || !got.BodyFetched {
		t.Fatalf("first body %+v", got)
	}
	if len(got.Headers) != 1 || got.Headers[0].Value != "live" {
		t.Fatalf("must keep metadata headers %#v", got.Headers)
	}
	if err := db.ApplyBodyUpdates(ctx, []BodyUpdate{{ID: "m1", Body: "second"}}, nil); err != nil {
		t.Fatal(err)
	}
	again, err := db.GetMessage(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if again.Body != "first" {
		t.Fatalf("concurrent body overwrite: %+v", again)
	}
}

func TestBodyUpdateFillsLegacyNullHeaders(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("m1", at)}); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyBodyUpdates(ctx, []BodyUpdate{{
		ID: "m1", Body: "b", Headers: []Header{{Name: "From", Value: "a@x.com"}},
	}}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Headers) != 1 || got.Headers[0].Name != "From" {
		t.Fatalf("legacy fill %#v", got.Headers)
	}
}

func TestBodyTombstoneExistingOnly(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("m1", at)}); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyBodyUpdates(ctx, nil, []string{"m1", "missing"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsDeleted || got.BodyFetched {
		t.Fatalf("tombstone %+v", got)
	}
	if _, err := db.GetMessage(ctx, "missing"); err == nil {
		t.Fatal("must not fabricate")
	}
}

func TestSQLWriteDirtiesAndLiveMembership(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	msg := sample("m1", at)
	msg.Subject = "originalxyz"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QuerySQL(ctx, "UPDATE messages SET subject = 'rewrittenxyz' WHERE id = 'm1'", true); err != nil {
		t.Fatal(err)
	}
	ok, err := db.HasFTS(ctx)
	if err != nil || ok {
		t.Fatalf("sql write must dirty fts %v %v", ok, err)
	}
	hits, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "rewrittenxyz"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "m1" {
		t.Fatalf("live membership %#v", ids(hits))
	}
	if err := db.EnsureFTS(ctx); err != nil {
		t.Fatal(err)
	}
	ok, err = db.HasFTS(ctx)
	if err != nil || !ok {
		t.Fatalf("ensure repair %v %v", ok, err)
	}
}

func TestEnsureFTSNoopWhenReady(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	if err := db.EnsureFTS(ctx); err != nil {
		t.Fatal(err)
	}
	ok, err := db.HasFTS(ctx)
	if err != nil || !ok {
		t.Fatalf("after ensure %v %v", ok, err)
	}
	if err := db.EnsureFTS(ctx); err != nil {
		t.Fatal(err)
	}
	ok, err = db.HasFTS(ctx)
	if err != nil || !ok {
		t.Fatalf("noop %v %v", ok, err)
	}
}

func TestFailedRebuildStaysDirty(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(dbParent(t), "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("m1", at)}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SearchDir(path), []byte("not-a-dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err == nil {
		t.Fatal("expected rebuild fail")
	}
	ready, err := db.HasFTS(ctx)
	if err != nil || ready {
		t.Fatalf("must not report ready %v %v", ready, err)
	}
}

func TestV2MigrationSafeAnchorAndIdempotent(t *testing.T) {
	path := writeV2Fixture(t)
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	assertMigratedV2(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = again.Close() })
	assertMigratedV2(t, again)
}

func assertMigratedV2(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	v, ok, err := db.GetState(ctx, StateSchemaVersion)
	if err != nil || !ok || v != "4" {
		t.Fatalf("version %q %v %v", v, ok, err)
	}
	phase, ok, err := db.GetState(ctx, "full_phase")
	if err != nil || !ok || phase != "list" {
		t.Fatalf("safe phase %q %v %v", phase, ok, err)
	}
	anchor, ok, err := db.GetState(ctx, "full_start_history_id")
	if err != nil || !ok || anchor != "99" {
		t.Fatalf("safe anchor %q %v %v", anchor, ok, err)
	}
	page, ok, err := db.GetState(ctx, "list_page_token")
	if err != nil || !ok || page != "p1" {
		t.Fatalf("list page %q %v %v", page, ok, err)
	}
	hid, ok, err := db.GetState(ctx, "history_id")
	if err != nil || !ok || hid != "42" {
		t.Fatalf("history %q %v %v", hid, ok, err)
	}
	stale, ok, err := db.GetState(ctx, "body_after_id")
	if err != nil || !ok || stale != "stale" {
		t.Fatalf("unknown key must stay %q %v %v", stale, ok, err)
	}
	sent, err := db.GetMessage(ctx, "sent")
	if err != nil {
		t.Fatal(err)
	}
	if !sent.IsOutgoing {
		t.Fatalf("SENT backfill %+v", sent)
	}
	empty, err := db.GetMessage(ctx, "empty")
	if err != nil {
		t.Fatal(err)
	}
	if empty.BodyFetched || empty.Headers != nil {
		t.Fatalf("requeue/null headers %+v", empty)
	}
	dirty, ok, err := db.GetState(ctx, stateFTSDirty)
	if err != nil || !ok || dirty != ftsDirtyValue {
		t.Fatalf("migration must mark fts dirty %q %v %v", dirty, ok, err)
	}
}

func TestUnsafeOrphanListCursorDiscarded(t *testing.T) {
	path := writeUnsafeCursorFixture(t)
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, ok, _ := db.GetState(ctx, "list_page_token"); ok {
		t.Fatal("orphan list cursor must be discarded")
	}
	if _, ok, _ := db.GetState(ctx, "history_page_token"); ok {
		t.Fatal("orphan history page must be discarded")
	}
	hid, ok, err := db.GetState(ctx, "history_id")
	if err != nil || !ok || hid != "7" {
		t.Fatalf("history_id must stay %q %v %v", hid, ok, err)
	}
	phase, ok, err := db.GetState(ctx, "full_phase")
	if err != nil || !ok || phase != "list_full" {
		t.Fatalf("must keep full-restart intent %q %v %v", phase, ok, err)
	}
	if _, ok, _ := db.GetState(ctx, "full_start_history_id"); ok {
		t.Fatal("unsafe anchor must be cleared")
	}
	note, ok, err := db.GetState(ctx, "custom_note")
	if err != nil || !ok || note != "keep-me" {
		t.Fatalf("unknown key %q %v %v", note, ok, err)
	}
	bodyCur, ok, err := db.GetState(ctx, "body_after_id")
	if err != nil || !ok || bodyCur != "stale" {
		t.Fatalf("unknown body key %q %v %v", bodyCur, ok, err)
	}
}

func TestDirtyWriteRollback(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("ok", at)}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `CREATE UNIQUE INDEX uq_from_email ON messages(from_email)`); err != nil {
		t.Fatal(err)
	}
	bad := sample("fail", at)
	if err := db.UpsertMessages(ctx, []Message{bad}); err == nil {
		t.Fatal("expected upsert fail")
	}
	ok, err := db.HasFTS(ctx)
	if err != nil || !ok {
		t.Fatalf("failed write must rollback dirty %v %v", ok, err)
	}
	if _, err := db.GetMessage(ctx, "fail"); err == nil {
		t.Fatal("rolled back insert must not exist")
	}
}

func writeV2Fixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(dbParent(t), "v2.duckdb")
	raw, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
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
  body_fetched BOOLEAN DEFAULT false,
  synced_at TIMESTAMP,
  search_text VARCHAR
);
CREATE TABLE sync_state (
  key VARCHAR PRIMARY KEY,
  value VARCHAR
);
INSERT INTO messages(id, thread_id, internal_date, from_name, from_email, to_emails, cc_emails, subject, snippet, body, size_bytes, label_ids, is_read, is_outgoing, is_deleted, has_body, body_fetched, synced_at)
VALUES
  ('sent', 't', TIMESTAMP '2024-01-01 00:00:00', '', 'alias@other.com', [], [], 'out', '', 'hi', 1, ['SENT'], true, false, false, true, true, TIMESTAMP '2024-01-01 00:00:00'),
  ('empty', 't', TIMESTAMP '2024-01-01 00:00:00', '', 'a@x.com', [], [], 'e', '', '', 1, ['INBOX'], true, false, false, false, true, TIMESTAMP '2024-01-01 00:00:00');
INSERT INTO sync_state(key, value) VALUES
  ('schema_version', '2'),
  ('history_id', '42'),
  ('full_phase', 'list'),
  ('full_start_history_id', '99'),
  ('list_page_token', 'p1'),
  ('body_after_id', 'stale'),
  ('profile_email', 'me@example.com'),
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

func writeUnsafeCursorFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(dbParent(t), "unsafe.duckdb")
	raw, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
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
  body_fetched BOOLEAN DEFAULT false,
  synced_at TIMESTAMP,
  search_text VARCHAR
);
CREATE TABLE sync_state (
  key VARCHAR PRIMARY KEY,
  value VARCHAR
);
INSERT INTO messages(id, thread_id, internal_date, from_name, from_email, to_emails, cc_emails, subject, snippet, body, size_bytes, label_ids, is_read, is_outgoing, is_deleted, has_body, body_fetched, synced_at)
VALUES ('m', 't', TIMESTAMP '2024-01-01 00:00:00', '', 'a@x.com', [], [], 's', '', 'hi', 1, ['INBOX'], true, false, false, true, true, TIMESTAMP '2024-01-01 00:00:00');
INSERT INTO sync_state(key, value) VALUES
  ('schema_version', '2'),
  ('history_id', '7'),
  ('list_page_token', 'orphan'),
  ('history_page_token', 'hp'),
  ('full_phase', 'unknown'),
  ('search_text_v1', '1'),
  ('body_after_id', 'stale'),
  ('custom_note', 'keep-me');
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

func TestCursorOnlyPageLeavesFTSClean(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("m1", at)}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.CommitSyncPage(ctx, PageCommit{HistoryPage: StateValue("p2")}); err != nil {
		t.Fatal(err)
	}
	ok, err := db.HasFTS(ctx)
	if err != nil || !ok {
		t.Fatalf("cursor-only page must not dirty %v %v", ok, err)
	}
}

func TestDirtySurvivesReopenThenEnsureFTS(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(dbParent(t), "reopen.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("m1", at)}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBody(ctx, "m1", "later"); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.HasFTS(ctx); err != nil || ok {
		t.Fatalf("dirty before close %v %v", ok, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = again.Close() })
	if ok, err := again.HasFTS(ctx); err != nil || ok {
		t.Fatalf("dirty after reopen %v %v", ok, err)
	}
	if err := again.EnsureFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, err := again.HasFTS(ctx); err != nil || !ok {
		t.Fatalf("ensure repair %v %v", ok, err)
	}
}

func TestRequeueEmptyBodyStaysFetchedAfterSaveAndReopen(t *testing.T) {
	ctx := context.Background()
	path := writeV2Fixture(t)
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := db.GetMessage(ctx, "empty")
	if err != nil {
		t.Fatal(err)
	}
	if empty.BodyFetched {
		t.Fatalf("must requeue once %+v", empty)
	}
	if err := db.UpdateBody(ctx, "empty", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = again.Close() })
	got, err := again.GetMessage(ctx, "empty")
	if err != nil {
		t.Fatal(err)
	}
	if !got.BodyFetched || got.HasBody {
		t.Fatalf("saved empty must stay fetched %+v", got)
	}
}

func TestCommitSyncPageRollbackAfterMailWrites(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	old := sample("old", at)
	if err := db.UpsertMessages(ctx, []Message{old}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `
CREATE TABLE sync_state_chk (
  key VARCHAR PRIMARY KEY,
  value VARCHAR,
  CHECK (key <> 'list_page_token' OR value <> 'FAILME')
);
INSERT INTO sync_state_chk SELECT * FROM sync_state;
DROP TABLE sync_state;
ALTER TABLE sync_state_chk RENAME TO sync_state;
`); err != nil {
		t.Fatal(err)
	}
	err := db.CommitSyncPage(ctx, PageCommit{
		Messages:   []Message{sample("new", at)},
		Tombstones: []string{"old"},
		Seen:       []string{"new"},
		ListPage:   StateValue("FAILME"),
	})
	if err == nil {
		t.Fatal("expected checkpoint fail")
	}
	if _, err := db.GetMessage(ctx, "new"); err == nil {
		t.Fatal("rolled back insert")
	}
	got, err := db.GetMessage(ctx, "old")
	if err != nil || got.IsDeleted {
		t.Fatalf("tombstone rolled back %+v %v", got, err)
	}
	var n int
	if err := db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM sync_seen").Scan(&n); err != nil || n != 0 {
		t.Fatalf("seen %d %v", n, err)
	}
	if _, ok, _ := db.GetState(ctx, "list_page_token"); ok {
		t.Fatal("cursor must stay unset")
	}
	ok, err := db.HasFTS(ctx)
	if err != nil || !ok {
		t.Fatalf("dirty rolled back %v %v", ok, err)
	}
}

func TestMutationGateHonorsDeadline(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	held := make(chan struct{})
	go func() {
		db.mu <- struct{}{}
		close(held)
		time.Sleep(time.Second)
		<-db.mu
	}()
	<-held
	short, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := db.UpsertMessages(short, []Message{sample("late", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))})
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("want ctx error, got %v after %s", err, elapsed)
	}
	if elapsed > 400*time.Millisecond {
		t.Fatalf("blocked on mutex %s", elapsed)
	}
	if _, err := db.GetMessage(ctx, "late"); err == nil {
		t.Fatal("must not write")
	}
}

func TestRebuildThenNewerWriteLeavesDirty(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	msg := sample("m1", at)
	msg.Subject = "oldxyz"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	later := sample("m2", at.Add(time.Hour))
	later.Subject = "newxyz"
	if err := db.UpsertMessages(ctx, []Message{later}); err != nil {
		t.Fatal(err)
	}
	hits, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "newxyz"})
	if err != nil || len(hits) != 1 || hits[0].ID != "m2" {
		t.Fatalf("literal membership %#v %v", ids(hits), err)
	}
	ok, err := db.HasFTS(ctx)
	if err != nil || ok {
		t.Fatalf("newer write must stay dirty %v %v", ok, err)
	}
}

func TestConcurrentRebuildAndWriteKeepMembership(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	msg := sample("m1", at)
	msg.Subject = "oldxyz"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() { done <- db.RebuildFTS(ctx) }()
	later := sample("m2", at.Add(time.Hour))
	later.Subject = "newxyz"
	go func() { done <- db.UpsertMessages(ctx, []Message{later}) }()
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	hits, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "newxyz"})
	if err != nil || len(hits) != 1 || hits[0].ID != "m2" {
		t.Fatalf("literal membership %#v %v", ids(hits), err)
	}
}
