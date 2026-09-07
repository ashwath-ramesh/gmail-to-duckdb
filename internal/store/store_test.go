package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "mail.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func sample(id string, at time.Time) Message {
	return Message{
		ID:           id,
		ThreadID:     "t-" + id,
		HistoryID:    10,
		InternalDate: at,
		FromName:     "Alice",
		FromEmail:    "alice@example.com",
		ToEmails:     []string{"bob@example.com"},
		CcEmails:     []string{"cc@example.com"},
		Subject:      "Hello " + id,
		Snippet:      "snippet " + id,
		SizeBytes:    100,
		LabelIDs:     []string{"INBOX", "UNREAD"},
		IsRead:       false,
		IsOutgoing:   false,
		SyncedAt:     at,
	}
}

func TestUpsertAndGet(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 2, 15, 4, 5, 0, time.UTC)
	msg := sample("m1", at)

	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "m1" || got.FromEmail != "alice@example.com" || got.Subject != "Hello m1" {
		t.Fatalf("got %+v", got)
	}
	if len(got.ToEmails) != 1 || got.ToEmails[0] != "bob@example.com" {
		t.Fatalf("to: %#v", got.ToEmails)
	}
	if len(got.LabelIDs) != 2 {
		t.Fatalf("labels: %#v", got.LabelIDs)
	}
	if got.HasBody || got.IsDeleted {
		t.Fatalf("flags: %+v", got)
	}
}

func TestUpsertPreservesBody(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 2, 15, 4, 5, 0, time.UTC)
	msg := sample("m1", at)
	body := "plain body"
	msg.Body = body
	msg.HasBody = true
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}

	meta := sample("m1", at)
	meta.Subject = "updated"
	if err := db.UpsertMessages(ctx, []Message{meta}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasBody || got.Body != body {
		t.Fatalf("body lost: %+v", got)
	}
	if got.Subject != "updated" {
		t.Fatalf("subject: %q", got.Subject)
	}
}

func TestListKeysetAndFilters(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	t1 := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 3, 2, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2024, 3, 3, 0, 0, 0, 0, time.UTC)
	a := sample("a", t1)
	b := sample("b", t2)
	b.FromEmail = "other@example.com"
	b.IsRead = true
	b.LabelIDs = []string{"INBOX"}
	c := sample("c", t3)
	c.IsDeleted = true
	if err := db.UpsertMessages(ctx, []Message{a, b, c}); err != nil {
		t.Fatal(err)
	}

	all, err := db.ListMessages(ctx, ListFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != "b" || all[1].ID != "a" {
		t.Fatalf("order: %#v", ids(all))
	}

	page, err := db.ListMessages(ctx, ListFilter{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	next, err := db.ListMessages(ctx, ListFilter{
		Limit:     1,
		AfterDate: page[0].InternalDate,
		AfterID:   page[0].ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].ID != "a" {
		t.Fatalf("keyset: %#v", ids(next))
	}

	unread, err := db.ListMessages(ctx, ListFilter{Limit: 10, Unread: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 1 || unread[0].ID != "a" {
		t.Fatalf("unread: %#v", ids(unread))
	}

	from, err := db.ListMessages(ctx, ListFilter{Limit: 10, From: "other@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(from) != 1 || from[0].ID != "b" {
		t.Fatalf("from: %#v", ids(from))
	}

	labeled, err := db.ListMessages(ctx, ListFilter{Limit: 10, Label: "UNREAD"})
	if err != nil {
		t.Fatal(err)
	}
	if len(labeled) != 1 || labeled[0].ID != "a" {
		t.Fatalf("label: %#v", ids(labeled))
	}
}

func TestMarkDeletedAndSeen(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("keep", at), sample("gone", at)}); err != nil {
		t.Fatal(err)
	}
	if err := db.ResetSeen(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.AddSeen(ctx, []string{"keep"}); err != nil {
		t.Fatal(err)
	}
	n, err := db.MarkMissingDeleted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("marked %d", n)
	}
	gone, err := db.GetMessage(ctx, "gone")
	if err != nil {
		t.Fatal(err)
	}
	if !gone.IsDeleted {
		t.Fatal("expected deleted")
	}
	keep, err := db.GetMessage(ctx, "keep")
	if err != nil {
		t.Fatal(err)
	}
	if keep.IsDeleted {
		t.Fatal("keep should stay")
	}
}

func TestStateAndMissingIDs(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	if err := db.SetState(ctx, "history_id", "99"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := db.GetState(ctx, "history_id")
	if err != nil || !ok || v != "99" {
		t.Fatalf("state %q %v %v", v, ok, err)
	}
	_, ok, err = db.GetState(ctx, "missing")
	if err != nil || ok {
		t.Fatalf("missing: %v %v", ok, err)
	}

	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("have", at)}); err != nil {
		t.Fatal(err)
	}
	missing, err := db.MissingIDs(ctx, []string{"have", "need"})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "need" {
		t.Fatalf("missing: %#v", missing)
	}
}

func TestBodyAndIDsWithoutBody(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("m1", at)}); err != nil {
		t.Fatal(err)
	}
	ids, err := db.IDsWithoutBody(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "m1" {
		t.Fatalf("ids: %#v", ids)
	}
	if err := db.UpdateBody(ctx, "m1", "the body"); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasBody || got.Body != "the body" {
		t.Fatalf("body: %+v", got)
	}
	hits, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "the body"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "m1" {
		t.Fatalf("body search: %d", len(hits))
	}
	ids, err = db.IDsWithoutBody(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected empty, got %#v", ids)
	}
}

func TestSearchTextWithoutBody(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	msg := sample("m1", at)
	msg.FromName = "Zelda"
	msg.FromEmail = "zelda@example.com"
	msg.ToEmails = []string{"link@hyrule.test"}
	msg.Subject = "master sword invoice"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}

	byName, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "Zelda"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byName) != 1 || byName[0].ID != "m1" {
		t.Fatalf("name: %#v", ids(byName))
	}

	byTo, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "link@hyrule.test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byTo) != 1 || byTo[0].ID != "m1" {
		t.Fatalf("to: %#v", ids(byTo))
	}

	bySub, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "invoice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(bySub) != 1 || bySub[0].ID != "m1" {
		t.Fatalf("subject: %#v", ids(bySub))
	}
}

func TestSearchOperatorsAndOffset(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	t1 := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	a := sample("a", t1)
	a.FromEmail = "ann@x.com"
	a.FromName = "Ann"
	a.IsRead = false
	b := sample("b", t2)
	b.FromEmail = "bob@x.com"
	b.FromName = "Bob"
	b.IsRead = true
	if err := db.UpsertMessages(ctx, []Message{a, b}); err != nil {
		t.Fatal(err)
	}

	from, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "from:ann"})
	if err != nil {
		t.Fatal(err)
	}
	if len(from) != 1 || from[0].ID != "a" {
		t.Fatalf("from: %#v", ids(from))
	}

	unread, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "unread"})
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 1 || unread[0].ID != "a" {
		t.Fatalf("unread: %#v", ids(unread))
	}

	after, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "after:2024-02-15"})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].ID != "b" {
		t.Fatalf("after: %#v", ids(after))
	}

	page, err := db.ListMessages(ctx, ListFilter{Limit: 1, Query: "from:@x.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ID != "b" {
		t.Fatalf("page: %#v", ids(page))
	}
	next, err := db.ListMessages(ctx, ListFilter{Limit: 1, Offset: 1, Query: "from:@x.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].ID != "a" {
		t.Fatalf("offset: %#v", ids(next))
	}
}

func TestSearchFTSRankAndFallback(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	weak := sample("weak", at)
	weak.Subject = "pineapple note"
	strong := sample("strong", at.Add(time.Hour))
	strong.Subject = "pineapple pineapple pineapple"
	if err := db.UpsertMessages(ctx, []Message{weak, strong}); err != nil {
		t.Fatal(err)
	}

	fallback, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "pineapple"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fallback) != 2 {
		t.Fatalf("fallback: %#v", ids(fallback))
	}

	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	hits, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "pineapple"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].ID != "strong" {
		t.Fatalf("rank: %#v", ids(hits))
	}
}

func TestEnsureFTS(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	ok, err := db.hasFTS(ctx)
	if err != nil || ok {
		t.Fatalf("fts before %v %v", ok, err)
	}
	if err := db.EnsureFTS(ctx); err != nil {
		t.Fatal(err)
	}
	ok, err = db.hasFTS(ctx)
	if err != nil || !ok {
		t.Fatalf("fts after %v %v", ok, err)
	}
	if err := db.SetState(ctx, stateFTSIndex, "old"); err != nil {
		t.Fatal(err)
	}
	db.ftsOK = false
	if err := db.EnsureFTS(ctx); err != nil {
		t.Fatal(err)
	}
	ver, has, err := db.GetState(ctx, stateFTSIndex)
	if err != nil || !has || ver != ftsIndexVer {
		t.Fatalf("ver %q %v %v", ver, has, err)
	}
}

func TestLikeWildcardsAreLiteral(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("a", at), sample("b", at.Add(time.Hour))}); err != nil {
		t.Fatal(err)
	}
	hits, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "from:%"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("wildcard: %#v", ids(hits))
	}
}

func TestLabelsAndFTS(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	if err := db.UpsertLabels(ctx, []Label{{ID: "INBOX", Name: "Inbox", Type: "system"}}); err != nil {
		t.Fatal(err)
	}
	m, err := db.LabelMap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m["INBOX"] != "Inbox" {
		t.Fatalf("map: %#v", m)
	}

	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	msg := sample("m1", at)
	msg.Subject = "unique pineapple subject"
	msg.Body = "the pineapple is ripe"
	msg.HasBody = true
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	hits, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "pineapple"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "m1" {
		t.Fatalf("fts: %#v", ids(hits))
	}
}

func TestUpdateLabels(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("m1", at)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateLabels(ctx, "m1", []string{"INBOX"}, true); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsRead || len(got.LabelIDs) != 1 || got.LabelIDs[0] != "INBOX" {
		t.Fatalf("labels: %+v", got)
	}
}

func TestExecSQL(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("m1", at)}); err != nil {
		t.Fatal(err)
	}
	res, err := db.ExecSQL(ctx, "SELECT id, subject, size_bytes FROM messages")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Columns) != 3 || len(res.Rows) != 1 {
		t.Fatalf("sql: %+v", res)
	}
	if res.Rows[0][0] != "m1" {
		t.Fatalf("id %#v", res.Rows[0][0])
	}
	switch res.Rows[0][2].(type) {
	case int32, int64, int:
	default:
		t.Fatalf("size type %T", res.Rows[0][2])
	}
}

func TestQuerySQLReadOnly(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	if _, err := db.QuerySQL(ctx, "SELECT 1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QuerySQL(ctx, "INSERT INTO labels(id, name, type) VALUES ('x', 'X', 'user')", false); err == nil {
		t.Fatal("expected write reject")
	}
	if _, err := db.QuerySQL(ctx, "INSERT INTO labels(id, name, type) VALUES ('x', 'X', 'user')", true); err != nil {
		t.Fatal(err)
	}
}

func TestCheckSQL(t *testing.T) {
	if err := CheckSQL("SELECT 1", false); err != nil {
		t.Fatal(err)
	}
	if err := CheckSQL("-- comment\nSELECT 1", false); err != nil {
		t.Fatal(err)
	}
	if err := CheckSQL("SELECT 1; DROP TABLE messages", false); err == nil {
		t.Fatal("expected multi-statement reject")
	}
	if err := CheckSQL("UPDATE messages SET subject = 'x'", false); err == nil {
		t.Fatal("expected write reject")
	}
	if err := CheckSQL("UPDATE messages SET subject = 'x'", true); err != nil {
		t.Fatal(err)
	}
	if err := CheckSQL("WITH d AS (DELETE FROM messages RETURNING id) SELECT count(*) FROM d", false); err == nil {
		t.Fatal("expected mutating CTE reject")
	}
	if err := CheckSQL("EXPLAIN ANALYZE DELETE FROM messages", false); err == nil {
		t.Fatal("expected explain analyze write reject")
	}
	if err := CheckSQL("SELECT * FROM read_csv('/tmp/x.csv')", false); err == nil {
		t.Fatal("expected file read reject")
	}
	if err := CheckSQL("SELECT * FROM messages WHERE subject = 'DELETE'", false); err != nil {
		t.Fatal(err)
	}
	if err := CheckSQL("WITH x AS (SELECT id FROM messages) SELECT * FROM x", false); err != nil {
		t.Fatal(err)
	}
}

func TestCoverageAndSchemaVersion(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	v, ok, err := db.GetState(ctx, StateSchemaVersion)
	if err != nil || !ok || v != "1" {
		t.Fatalf("schema version %q %v %v", v, ok, err)
	}
	c, err := db.Coverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.Total != 0 || c.SearchCovers() != "metadata" {
		t.Fatalf("%+v", c)
	}
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	m1 := sample("m1", at)
	m2 := sample("m2", at)
	m2.HasBody = true
	m2.Body = "hi"
	if err := db.UpsertMessages(ctx, []Message{m1, m2}); err != nil {
		t.Fatal(err)
	}
	c, err = db.Coverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.Total != 2 || c.WithBody != 1 || c.SearchCovers() != "mixed" {
		t.Fatalf("%+v", c)
	}
	tables, err := db.DescribeSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) < 3 {
		t.Fatalf("tables %#v", tables)
	}
}

func ids(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}
