package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(dbParent(t), "mail.duckdb"))
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

func TestBodyAndIDsNeedingFetch(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("m1", at)}); err != nil {
		t.Fatal(err)
	}
	ids, err := db.IDsNeedingFetch(ctx, "", 10)
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
	ids, err = db.IDsNeedingFetch(ctx, "", 10)
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
	if err := db.SetState(ctx, stateSearchCacheID, "old"); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureFTS(ctx); err != nil {
		t.Fatal(err)
	}
	ok, err = db.hasFTS(ctx)
	if err != nil || !ok {
		t.Fatalf("ensure after identity change %v %v", ok, err)
	}
}

func TestConcurrentHasFTSRebuildList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	msg := sample("m1", at)
	msg.Subject = "pineapple note"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errc := make(chan error, 3)
	start := make(chan struct{})
	run := func(fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 8; i++ {
				if err := fn(); err != nil {
					errc <- err
					return
				}
			}
		}()
	}
	run(func() error {
		_, err := db.HasFTS(ctx)
		return err
	})
	run(func() error {
		return db.RebuildFTS(ctx)
	})
	run(func() error {
		hits, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "pineapple"})
		if err != nil {
			return err
		}
		if len(hits) != 1 || hits[0].ID != "m1" {
			return errors.New("search lost seeded message")
		}
		return nil
	})
	close(start)
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}
}

func TestHasFTSAfterCorruptSearchCache(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(dbParent(t), "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
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
		t.Fatalf("fts after rebuild %v %v", ok, err)
	}
	if err := db.idx.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(SearchDir(path), "index.sqlite"), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, err = db.HasFTS(ctx)
	if err != nil || ok {
		t.Fatalf("fts after corrupt %v %v", ok, err)
	}
	hits, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "pineapple"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "m1" {
		t.Fatalf("like fallback after corrupt: %#v", ids(hits))
	}
	short, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if len(short) != 1 || short[0].ID != "m1" {
		t.Fatalf("short query after corrupt: %#v", ids(short))
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

func TestQuerySQLGuards(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	if err := db.UpsertLabels(ctx, []Label{{ID: "INBOX", Name: "Inbox", Type: "system"}}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("m1", at)}); err != nil {
		t.Fatal(err)
	}

	mustLabels := func(t *testing.T) {
		t.Helper()
		m, err := db.LabelMap(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if m["INBOX"] != "Inbox" {
			t.Fatalf("labels lost: %#v", m)
		}
	}

	if _, err := db.QuerySQL(ctx, `SELECT 1 AS "--"; DELETE FROM labels`, false); err == nil {
		t.Fatal("expected exploit reject")
	}
	mustLabels(t)

	if _, err := db.QuerySQL(ctx, `SELECT 1 AS "--"; DELETE FROM labels`, true); err == nil {
		t.Fatal("expected multi-statement reject with --write")
	}
	mustLabels(t)

	if _, err := db.QuerySQL(ctx, "SELECT 1; DROP TABLE labels", false); err == nil {
		t.Fatal("expected multi-statement reject")
	}
	mustLabels(t)

	res, err := db.QuerySQL(ctx, `SELECT 1 AS "--", ';' AS semi, 'DELETE' AS w`, false)
	if err != nil || len(res.Rows) != 1 {
		t.Fatalf("quoted delimiters: %+v %v", res, err)
	}
	if _, err := db.QuerySQL(ctx, "-- comment\nSELECT 1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QuerySQL(ctx, "SELECT 1 /* block */", false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QuerySQL(ctx, "SELECT 1 /* outer /* nested */ */", false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QuerySQL(ctx, "PIVOT labels ON type USING count(id)", false); err == nil {
		t.Fatal("expected dynamic PIVOT reject")
	}
	if _, err := db.QuerySQL(ctx, "SELECT * FROM messages WHERE subject = 'DELETE'", false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QuerySQL(ctx, "WITH x AS (SELECT id FROM messages) SELECT * FROM x", false); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{
		"SHOW TABLES",
		"DESCRIBE labels",
		"DESC labels",
		"SUMMARIZE labels",
		"FROM labels",
		"VALUES (1)",
		"FROM labels PIVOT (count(id) FOR type IN ('system', 'user'))",
		"EXPLAIN SELECT 1",
		"EXPLAIN ANALYZE SELECT 1",
	} {
		if _, err := db.QuerySQL(ctx, q, false); err != nil {
			t.Fatalf("read %q: %v", q, err)
		}
	}

	if _, err := db.QuerySQL(ctx, "UPDATE messages SET subject = 'x'", false); err == nil {
		t.Fatal("expected write reject")
	}
	if _, err := db.QuerySQL(ctx, "WITH d AS (DELETE FROM labels RETURNING id) SELECT count(*) FROM d", false); err == nil {
		t.Fatal("expected mutating CTE reject")
	}
	mustLabels(t)
	if _, err := db.QuerySQL(ctx, "EXPLAIN ANALYZE DELETE FROM labels", false); err == nil {
		t.Fatal("expected explain analyze write reject")
	}
	mustLabels(t)

	if _, err := db.QuerySQL(ctx, "", false); err == nil {
		t.Fatal("expected empty reject")
	}
	if _, err := db.QuerySQL(ctx, "   \n\t", false); err == nil {
		t.Fatal("expected whitespace reject")
	}
	if _, err := db.QuerySQL(ctx, "SELECT 1\x00DELETE FROM labels", false); err == nil {
		t.Fatal("expected NUL reject")
	} else if !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("nul diagnostic: %v", err)
	}
	mustLabels(t)

	if _, err := db.QuerySQL(ctx, "UPDATE messages SET subject = 'x'", true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QuerySQL(ctx, "BEGIN", true); err == nil {
		t.Fatal("expected transaction reject")
	}
	if _, err := db.QuerySQL(ctx, "CREATE SEQUENCE sql_seq", true); err != nil {
		t.Fatal(err)
	}
	first, err := db.QuerySQL(ctx, "SELECT nextval('sql_seq')", true)
	if err != nil || len(first.Rows) != 1 {
		t.Fatalf("nextval write: %+v %v", first, err)
	}
	if _, err := db.QuerySQL(ctx, "SELECT nextval('sql_seq')", false); err == nil {
		t.Fatal("expected nextval reject in read-only")
	}
	second, err := db.QuerySQL(ctx, "SELECT nextval('sql_seq')", true)
	if err != nil || len(second.Rows) != 1 {
		t.Fatalf("nextval after ro: %+v %v", second, err)
	}
	if second.Rows[0][0] != int64(2) && second.Rows[0][0] != int32(2) {
		t.Fatalf("sequence advanced during read-only: first=%v second=%v", first.Rows[0][0], second.Rows[0][0])
	}

	csv := filepath.Join(t.TempDir(), "ok.csv")
	if err := os.WriteFile(csv, []byte("n\n1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readCSV := "SELECT * FROM read_csv('" + strings.ReplaceAll(csv, "'", "''") + "')"
	if _, err := db.QuerySQL(ctx, readCSV, false); err == nil {
		t.Fatal("expected external read reject")
	}
	if _, err := db.QuerySQL(ctx, readCSV, true); err == nil {
		t.Fatal("expected external read reject with --write")
	}
	if _, err := os.Stat(csv); err != nil {
		t.Fatal(err)
	}

	if _, err := db.QuerySQL(ctx, "SET enable_external_access = true", true); err == nil {
		t.Fatal("expected setting reject")
	}
	if _, err := db.QuerySQL(ctx, "SET lock_configuration = false", true); err == nil {
		t.Fatal("expected setting reject")
	}
	if _, err := db.QuerySQL(ctx, readCSV, true); err == nil {
		t.Fatal("external access must stay off")
	}
}

func TestQuerySQLExplainAnalyzeBlockedSideEffects(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	dir := t.TempDir()
	copyPath := filepath.Join(dir, "explain_copy.csv")
	attachPath := filepath.Join(dir, "explain_attach.duckdb")
	csvPath := filepath.Join(dir, "sentinel.csv")
	if err := os.WriteFile(csvPath, []byte("n\nEXPLAIN_CALL_SENTINEL\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	qCopy := "EXPLAIN ANALYZE COPY (SELECT 1 AS n) TO '" + strings.ReplaceAll(copyPath, "'", "''") + "'"
	qAttach := "EXPLAIN ANALYZE ATTACH '" + strings.ReplaceAll(attachPath, "'", "''") + "' AS explain_extra"
	qCall := "EXPLAIN ANALYZE CALL read_csv('" + strings.ReplaceAll(csvPath, "'", "''") + "')"

	threadsBefore := currentSetting(t, db, "threads")
	wantThreads := "7"
	if threadsBefore == wantThreads {
		wantThreads = "8"
	}

	if _, err := db.QuerySQL(ctx, qCopy, true); err == nil {
		t.Fatal("expected EXPLAIN ANALYZE COPY error")
	}
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatalf("copy target: %v", err)
	}

	if _, err := db.QuerySQL(ctx, qAttach, true); err == nil {
		t.Fatal("expected EXPLAIN ANALYZE ATTACH error")
	}
	if _, err := os.Stat(attachPath); !os.IsNotExist(err) {
		t.Fatalf("attach target: %v", err)
	}
	dbs, err := db.QuerySQL(ctx, "SELECT database_name FROM duckdb_databases()", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range dbs.StringRows() {
		if len(row) > 0 && row[0] == "explain_extra" {
			t.Fatal("EXPLAIN ANALYZE ATTACH must not attach a database")
		}
	}

	if _, err := db.QuerySQL(ctx, "EXPLAIN ANALYZE SET threads = "+wantThreads, true); err == nil {
		t.Fatal("expected EXPLAIN ANALYZE SET error")
	}
	if got := currentSetting(t, db, "threads"); got != threadsBefore {
		t.Fatalf("EXPLAIN ANALYZE SET changed threads from %s to %s", threadsBefore, got)
	}

	if res, err := db.QuerySQL(ctx, qCall, true); err == nil {
		t.Fatalf("expected EXPLAIN ANALYZE CALL read_csv error, got %+v", res)
	}
	if _, err := db.QuerySQL(ctx, "CALL duckdb_settings()", true); err == nil {
		t.Fatal("expected bare CALL reject")
	}
}

func TestQuerySQLExplainAnalyzeWriteDML(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	if err := db.UpsertLabels(ctx, []Label{{ID: "TMP", Name: "Tmp", Type: "user"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QuerySQL(ctx, "EXPLAIN ANALYZE DELETE FROM labels WHERE id = 'TMP'", true); err != nil {
		t.Fatal(err)
	}
	labels, err := db.LabelMap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := labels["TMP"]; ok {
		t.Fatal("EXPLAIN ANALYZE DELETE must remove TMP")
	}
	if _, err := db.QuerySQL(ctx, "EXPLAIN ANALYZE SELECT 1", false); err != nil {
		t.Fatal(err)
	}
}

func currentSetting(t *testing.T, db *DB, name string) string {
	t.Helper()
	res, err := db.QuerySQL(context.Background(), "SELECT current_setting('"+name+"')", false)
	if err != nil {
		t.Fatal(err)
	}
	rows := res.StringRows()
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("setting %s: %+v", name, res)
	}
	return rows[0][0]
}

func TestQuerySQLConnRecover(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	if _, err := db.QuerySQL(ctx, "SELECT * FROM definitely_missing_table", false); err == nil {
		t.Fatal("expected read error")
	}
	if _, err := db.QuerySQL(ctx, "SELECT 1 AS n", false); err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.QuerySQL(canceled, "SELECT 1 AS n", false); err == nil {
		t.Fatal("expected canceled context error")
	}
	if _, err := db.QuerySQL(ctx, "INSERT INTO labels(id, name, type) VALUES ('after_cancel', 'After', 'user')", true); err != nil {
		t.Fatal(err)
	}

	slow, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	_, err := db.QuerySQL(slow, "SELECT sum(i) FROM range(2000000000) t(i)", false)
	if err == nil {
		t.Fatal("expected cancellation")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancel, got %v", err)
	}
	if _, err := db.QuerySQL(ctx, "INSERT INTO labels(id, name, type) VALUES ('after_timeout', 'After', 'user')", true); err != nil {
		t.Fatal(err)
	}
}

func TestFTSBootstrapOptional(t *testing.T) {
	if err := bootstrapTrusted(failExec{needle: "fts"}, Options{}); err != nil {
		t.Fatalf("fts load must not run: %v", err)
	}
	if err := bootstrapTrusted(failExec{needle: "ui"}, Options{DuckUI: true}); err == nil {
		t.Fatal("expected duckdb ui failure")
	}
}

func TestSQLUsableWhenIndexUnavailable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(dbParent(t), "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := os.WriteFile(SearchDir(path), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err == nil {
		t.Fatal("expected index unavailable")
	}
	if _, err := db.QuerySQL(ctx, "SELECT 1 AS n", false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "hello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Coverage(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestReconnectAfterLock(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	c, err := db.sql.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	discardConn(c)
	if err := c.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
		t.Fatal(err)
	}
	if _, err := db.QuerySQL(ctx, "SELECT 1 AS n", false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QuerySQL(ctx, "INSERT INTO labels(id, name, type) VALUES ('reconn', 'R', 'user')", true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QuerySQL(ctx, "SET enable_external_access = true", true); err == nil {
		t.Fatal("config must stay locked")
	}
	csv := filepath.Join(t.TempDir(), "ok.csv")
	if err := os.WriteFile(csv, []byte("n\n1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	q := "SELECT * FROM read_csv('" + strings.ReplaceAll(csv, "'", "''") + "')"
	if _, err := db.QuerySQL(ctx, q, true); err == nil {
		t.Fatal("external access must stay off")
	}
}

type failExec struct{ needle string }

func (f failExec) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(strings.ToLower(query), f.needle) {
		return nil, errors.New(f.needle + " unavailable")
	}
	return stubResult{}, nil
}

type stubResult struct{}

func (stubResult) LastInsertId() (int64, error) { return 0, nil }
func (stubResult) RowsAffected() (int64, error) { return 0, nil }

func TestQuerySQLConcurrentWithStoreWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("m1", at)}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() {
		_, err := db.QuerySQL(ctx, "SELECT count(*) FROM messages", false)
		done <- err
	}()
	go func() {
		done <- db.UpsertMessages(ctx, []Message{sample("m2", at.Add(time.Hour))})
	}()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("timeout")
		}
	}
}

func TestCoverageAndSchemaVersion(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	v, ok, err := db.GetState(ctx, StateSchemaVersion)
	if err != nil || !ok || v != "4" {
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
