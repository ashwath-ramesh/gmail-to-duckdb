package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/search"
)

func TestSearchCacheRejectsSymlinkDir(t *testing.T) {
	path := filepath.Join(dbParent(t), "mail.duckdb")
	target := filepath.Join(t.TempDir(), "other")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, SearchDir(path)); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("expected symlink search dir reject")
	}
}

func TestSearchCacheRejectsSymlinkIndex(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(dbParent(t), "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	msg := sample("m1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	msg.Subject = "pineapple"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(SearchDir(path), indexFileName)
	stash := live + ".real"
	if err := os.Rename(live, stash); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stash, live); err != nil {
		t.Skipf("symlink: %v", err)
	}
	again, err := Open(path)
	if err == nil {
		_ = again.Close()
		t.Fatal("expected symlink index reject on open")
	}
}

func TestSqliteDSNEscapesHashAndPercent(t *testing.T) {
	got := sqliteDSN(`/tmp/foo#bar%.sqlite`)
	if !strings.Contains(got, "foo%23bar%25.sqlite") {
		t.Fatalf("dsn %s", got)
	}
	if strings.Contains(got, "foo#bar") {
		t.Fatalf("unescaped hash %s", got)
	}
}

func TestWatermarkAheadForcesRebuild(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	msg := sample("m1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	msg.Subject = "aheadtoken"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.idx.setWatermark(ctx, 1<<40); err != nil {
		t.Fatal(err)
	}
	st, err := db.SearchIndexState(ctx)
	if err != nil || st == IndexStateReady {
		t.Fatalf("state %s %v", st, err)
	}
	if err := db.EnsureFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.HasFTS(ctx); err != nil || !ok {
		t.Fatalf("rebuilt %v %v", ok, err)
	}
	if got := indexedIDs(t, db, "aheadtoken"); len(got) != 1 {
		t.Fatalf("hits %#v", got)
	}
}

func TestCandidateCorruptionMarksUnusable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(dbParent(t), "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	msg := sample("m1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	msg.Subject = "corrtoken"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.idx.sql.Exec(`DROP TABLE fts`); err != nil {
		t.Fatal(err)
	}
	hits, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: "corrtoken"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "m1" {
		t.Fatalf("fallback %#v", ids(hits))
	}
	if _, err := db.indexedSearch(ctx, ListFilter{Limit: 10, Query: "corrtoken"}, mustParse(t, "corrtoken")); !errors.Is(err, errIndexSkip) {
		t.Fatalf("corrupt indexed %v", err)
	}
	db.idx.mu.Lock()
	broken := db.idx.broken
	db.idx.mu.Unlock()
	if !broken {
		t.Fatal("cache must be marked unusable")
	}
}

func mustParse(t *testing.T, q string) search.Query {
	t.Helper()
	parsed, err := search.Parse(q)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
