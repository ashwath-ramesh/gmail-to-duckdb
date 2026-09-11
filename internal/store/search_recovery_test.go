package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/search"
)

func TestIndexSurvivesReopenAndDelta(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(dbParent(t), "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a := sample("a", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	a.Subject = "reopentoken"
	if err := db.UpsertMessages(ctx, []Message{a}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	b := sample("b", time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC))
	b.Subject = "reopentoken"
	if err := db.UpsertMessages(ctx, []Message{b}); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureFTS(ctx); err != nil {
		t.Fatal(err)
	}
	got := indexedIDs(t, db, "reopentoken")
	if len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("hits %#v", got)
	}
}

func TestIndexDeleteAndLabelNoTextChange(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	msg := sample("m1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	msg.Subject = "deltoken"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateLabels(ctx, "m1", []string{"INBOX"}, true); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.HasFTS(ctx); err != nil || !ok {
		t.Fatalf("labels must not dirty index %v %v", ok, err)
	}
	if err := db.MarkDeleted(ctx, []string{"m1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(t, db, "deltoken"); len(got) != 0 {
		t.Fatalf("deleted still visible %#v", got)
	}
}

func TestWriterReacquireAfterDiscard(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	if db.writer == nil {
		t.Fatal("writer missing")
	}
	discardConn(db.writer)
	if err := db.UpsertMessages(ctx, []Message{sample("m1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "m1")
	if err != nil || got.ID != "m1" {
		t.Fatalf("reacquire %v %+v", err, got)
	}
}

func TestConcurrentIndexAndWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var seed []Message
	for i := 0; i < 40; i++ {
		m := sample("s"+strconv.Itoa(i), at.Add(time.Duration(i)*time.Minute))
		m.Subject = "conctoken " + strconv.Itoa(i)
		seed = append(seed, m)
	}
	if err := db.UpsertMessages(ctx, seed); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errc := make(chan error, 3)
	run := func(fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 6; i++ {
				if err := fn(); err != nil {
					errc <- err
					return
				}
			}
		}()
	}
	run(func() error { return db.RebuildFTS(ctx) })
	run(func() error {
		m := sample("w"+strconv.Itoa(time.Now().Nanosecond()%1000), at)
		m.Subject = "conctoken extra"
		return db.UpsertMessages(ctx, []Message{m})
	})
	run(func() error {
		hits, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "conctoken"})
		if err != nil {
			return err
		}
		if len(hits) == 0 {
			return errors.New("search lost rows")
		}
		return nil
	})
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}
}

func TestHealthAvailableDuringHeldSearch(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	msg := sample("m1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	msg.Subject = "holdtoken"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	db.testHoldDuck = func() {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
	}
	errc := make(chan error, 1)
	go func() {
		_, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "holdtoken"})
		errc <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("search did not pause")
	}
	if _, err := db.Coverage(ctx); err != nil {
		close(release)
		t.Fatalf("coverage: %v", err)
	}
	if _, err := db.SearchIndexState(ctx); err != nil {
		close(release)
		t.Fatalf("status: %v", err)
	}
	close(release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func TestMigrateSearchDoesNotRewriteCorpus(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(dbParent(t), "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	msg := sample("m1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	msg.Subject = "migtoken"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.ClearState(ctx, stateSearchText); err != nil {
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
	dirty, ok, err := again.GetState(ctx, stateFTSDirty)
	if err != nil || !ok || dirty != ftsDirtyValue {
		t.Fatalf("repair marker %q %v %v", dirty, ok, err)
	}
	if got := searchIDs(t, again, "migtoken"); len(got) != 1 {
		t.Fatalf("literal after migrate %#v", got)
	}
}

func TestSearchDirPrivateAfterBuild(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(dbParent(t), "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	msg := sample("m1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	msg.Subject = "privtoken"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	dir := SearchDir(path)
	if err := inspectCacheDir(dir, true); err != nil {
		t.Fatal(err)
	}
}

func TestIncompleteMultiBatchReopenReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(dbParent(t), "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	var msgs []Message
	for i := 0; i < 300; i++ {
		m := sample("p"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Minute))
		m.Subject = "replaytoken " + strconv.Itoa(i)
		msgs = append(msgs, m)
	}
	if err := db.UpsertMessages(ctx, msgs); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.idx.setWatermark(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st, err := db.SearchIndexState(ctx)
	if err != nil || st != IndexStatePending {
		t.Fatalf("reopen pending %s %v", st, err)
	}
	if err := db.EnsureFTS(ctx); err != nil {
		t.Fatal(err)
	}
	got := indexedIDs(t, db, "replaytoken")
	if len(got) != 50 {
		t.Fatalf("replay hits %d", len(got))
	}
}

// readFreshness is the first DuckDB table read and pins the whole database
// snapshot. Delta candidates and verify share that same transaction.
func TestDuckDBSnapshotAfterFreshnessAndDelta(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	old := sample("old", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	old.Subject = "snaptoken"
	newer := sample("new", time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC))
	newer.Subject = "snaptoken"
	if err := db.UpsertMessages(ctx, []Message{old, newer}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	db.testHoldDuck = func() {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
	}
	parsed, err := search.Parse("snaptoken")
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	var hits []Message
	go func() {
		var err error
		hits, err = db.indexedSearch(ctx, ListFilter{Limit: 10, Query: "snaptoken"}, parsed)
		errc <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("did not reach duck snapshot")
	}
	old.InternalDate = time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	extra := sample("extra", time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC))
	extra.Subject = "snaptoken"
	if err := db.UpsertMessages(ctx, []Message{old, extra}); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	got := ids(hits)
	if strings.Join(got, ",") != "new,old" {
		t.Fatalf("snapshot membership/order %#v", got)
	}
}

func TestReaderSnapshotIgnoresPartialIndexRewrite(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	old := sample("old", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	old.Subject = "snaptoken"
	if err := db.UpsertMessages(ctx, []Message{old}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	db.idx.holdCandidates = func() {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
	}
	parsed, err := search.Parse("snaptoken")
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	var hits []Message
	go func() {
		var err error
		hits, err = db.indexedSearch(ctx, ListFilter{Limit: 10, Query: "snaptoken"}, parsed)
		errc <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("did not reach sqlite snapshot")
	}
	if _, err := db.idx.sql.ExecContext(ctx, "DELETE FROM fts"); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "old" {
		t.Fatalf("sqlite snapshot missed old %#v", ids(hits))
	}
	db.idx.holdCandidates = nil
}

func TestCancelDuringCandidatesDoesNotRebuild(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	msg := sample("m1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	msg.Subject = "canceltoken"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	db.idx.holdCandidates = func() {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
	}
	parsed, err := search.Parse("canceltoken")
	if err != nil {
		t.Fatal(err)
	}
	req, cancel := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() {
		_, err := db.indexedSearch(req, ListFilter{Limit: 10, Query: "canceltoken"}, parsed)
		errc <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		cancel()
		close(release)
		t.Fatal("did not reach candidates")
	}
	cancel()
	close(release)
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
	db.idx.mu.Lock()
	broken := db.idx.broken
	db.idx.mu.Unlock()
	if broken {
		t.Fatal("cancel marked cache broken")
	}
	if ok, err := db.HasFTS(ctx); err != nil || !ok {
		t.Fatalf("ready after cancel %v %v", ok, err)
	}
	db.idx.holdCandidates = nil
	if got := indexedIDs(t, db, "canceltoken"); len(got) != 1 {
		t.Fatalf("after cancel %#v", got)
	}
}

func TestPublishCancelWhilePinned(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	msg := sample("m1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	msg.Subject = "publishtoken"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	db.idx.holdCandidates = func() {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
	}
	parsed, err := search.Parse("publishtoken")
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() {
		_, err := db.indexedSearch(ctx, ListFilter{Limit: 10, Query: "publishtoken"}, parsed)
		errc <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("did not pin")
	}
	stage := filepath.Join(db.idx.dir, indexBuildName)
	sdb, err := sqlOpenIndex(stage)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if err := initIndexSchema(ctx, sdb); err != nil {
		_ = sdb.Close()
		close(release)
		t.Fatal(err)
	}
	if err := sdb.Close(); err != nil {
		close(release)
		t.Fatal(err)
	}
	if err := privfile.Harden(stage); err != nil {
		close(release)
		t.Fatal(err)
	}
	pubCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	err = db.idx.publishFile(pubCtx, stage)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		close(release)
		t.Fatalf("publish %v", err)
	}
	close(release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	db.idx.holdCandidates = nil
	if got := indexedIDs(t, db, "publishtoken"); len(got) != 1 {
		t.Fatalf("after publish cancel %#v", got)
	}
}

func TestReplaceUnavailableFallsBack(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	msg := sample("m1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	msg.Subject = "replacetoken"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	db.idx.mu.Lock()
	db.idx.replacing = true
	db.idx.mu.Unlock()
	parsed, err := search.Parse("replacetoken")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.indexedSearch(ctx, ListFilter{Limit: 10, Query: "replacetoken"}, parsed)
	if !errors.Is(err, errIndexSkip) {
		t.Fatalf("replace %v", err)
	}
	got := searchIDs(t, db, "replacetoken")
	if len(got) != 1 {
		t.Fatalf("literal fallback %#v", got)
	}
	db.idx.mu.Lock()
	broken := db.idx.broken
	db.idx.replacing = false
	db.idx.mu.Unlock()
	if broken {
		t.Fatal("replace must not mark broken")
	}
}

func TestEmptyIDKeysetDoesNotLoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db := testDB(t)
	if _, err := db.QuerySQL(ctx, `
INSERT INTO messages (
  id, thread_id, internal_date, from_name, from_email, to_emails, cc_emails,
  subject, snippet, body, size_bytes, label_ids,
  is_read, is_outgoing, is_deleted, has_body, body_fetched, synced_at, search_text, search_revision
) VALUES (
  '', 't', TIMESTAMP '2024-01-01', '', '', [], [],
  'emptytoken', 'sn', '', 0, [],
  false, false, false, false, false, TIMESTAMP '2024-01-01', 'emptytoken', 1
)
`, true); err != nil {
		t.Fatal(err)
	}
	a := sample("a", time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC))
	a.Subject = "emptytoken"
	if err := db.UpsertMessages(ctx, []Message{a}); err != nil {
		t.Fatal(err)
	}
	if err := markFTSDirty(ctx, db.sql); err != nil {
		t.Fatal(err)
	}
	if err := db.repairSearchText(ctx); err != nil {
		t.Fatal(err)
	}
	parsed, err := search.Parse("emptytoken")
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.literalSearch(ctx, ListFilter{Limit: 50, Query: "emptytoken"}, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 1 {
		t.Fatalf("hits %#v", ids(got))
	}
}

func sqlOpenIndex(path string) (*sql.DB, error) {
	return sql.Open("sqlite", sqliteDSN(path))
}
