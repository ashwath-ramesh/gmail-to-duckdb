package store

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/search"
)

func TestIndexedSearchPathNotSilentFallback(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	seedSearchMsgs(t, db)
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	got := indexedIDs(t, db, "invoice leftover")
	if strings.Join(sortedCopy(got), ",") != "and-phrase,and-split" {
		t.Fatalf("hits %#v", got)
	}
}

func TestIndexedSearchMixedShortAndLongTerms(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	seedSearchMsgs(t, db)
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	got := indexedIDs(t, db, "a pineapple")
	if len(got) != 1 || got[0] != "pine-apple" {
		t.Fatalf("hits %#v", got)
	}
}

func TestIndexedSearchEmbeddedNUL(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	msg := sample("nul-1", at)
	msg.Subject = "prefix"
	msg.Body = "hello\x00worldtoken"
	msg.HasBody = true
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	got := indexedIDs(t, db, "worldtoken")
	if len(got) != 1 || got[0] != "nul-1" {
		t.Fatalf("hits %#v", got)
	}
}

func TestIndexedDateChangeUpdatesOrder(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	old := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(2 * time.Hour)
	a := sample("a", old)
	a.Subject = "datetoken"
	b := sample("b", old.Add(time.Hour))
	b.Subject = "datetoken"
	if err := db.UpsertMessages(ctx, []Message{a, b}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	got := indexedIDs(t, db, "datetoken")
	if strings.Join(got, ",") != "b,a" {
		t.Fatalf("before %#v", got)
	}
	a.InternalDate = newer
	if err := db.UpsertMessages(ctx, []Message{a}); err != nil {
		t.Fatal(err)
	}
	got = indexedIDs(t, db, "datetoken")
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("delta before EnsureFTS %#v", got)
	}
	if err := db.EnsureFTS(ctx); err != nil {
		t.Fatal(err)
	}
	got = indexedIDs(t, db, "datetoken")
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("after EnsureFTS %#v", got)
	}
	page := mustIndexed(t, db, "datetoken", ListFilter{Limit: 1, Offset: 1})
	if len(page) != 1 || page[0].ID != "b" {
		t.Fatalf("page %#v", ids(page))
	}
}

func TestIndexedFilterPastVerifyBatch(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	var msgs []Message
	for i := 0; i < 300; i++ {
		m := sample("n"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Minute))
		m.Subject = "batchtoken note"
		m.IsRead = i%10 != 0
		msgs = append(msgs, m)
	}
	if err := db.UpsertMessages(ctx, msgs); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	hits := mustIndexed(t, db, "batchtoken", ListFilter{Unread: true, Limit: 20, Offset: 10})
	if len(hits) != 20 {
		t.Fatalf("len %d", len(hits))
	}
	for _, h := range hits {
		if h.IsRead {
			t.Fatalf("read slipped through %s", h.ID)
		}
	}
}

func TestExplainAnalyzeUpdateInvalidatesIndex(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	msg := sample("m1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	msg.Subject = "originalxyz"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QuerySQL(ctx, "EXPLAIN ANALYZE UPDATE messages SET subject = 'rewrittenxyz' WHERE id = 'm1'", true); err != nil {
		t.Fatal(err)
	}
	ok, err := db.HasFTS(ctx)
	if err != nil || ok {
		t.Fatalf("must invalidate %v %v", ok, err)
	}
	if got := searchIDs(t, db, "rewrittenxyz"); len(got) != 1 || got[0] != "m1" {
		t.Fatalf("live %#v", got)
	}
	if got := searchIDs(t, db, "originalxyz"); len(got) != 0 {
		t.Fatalf("stale %#v", got)
	}
	if err := db.EnsureFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if got := indexedIDs(t, db, "rewrittenxyz"); len(got) != 1 {
		t.Fatalf("repaired hits %#v", got)
	}
}

func TestConnSettingsVerifiedAfterLock(t *testing.T) {
	db := testDB(t)
	if got := currentSetting(t, db, "enable_external_access"); !settingFalse(got) {
		t.Fatalf("external access %q", got)
	}
	if got := currentSetting(t, db, "lock_configuration"); !settingTrue(got) {
		t.Fatalf("lock %q", got)
	}
	ctx := context.Background()
	c, err := db.sql.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := db.verifyConn(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QuerySQL(ctx, "SET enable_external_access = true", true); err == nil {
		t.Fatal("lock must hold")
	}
}

func TestAccelClearsOnNormalOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(dbParent(t), "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	msg := sample("m1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	msg.Subject = "acceltoken"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.DisableSearchAccel(ctx); err != nil {
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
	if v, ok, err := again.GetState(ctx, stateSearchAccel); err != nil || (ok && v == searchAccelOff) {
		t.Fatalf("accel persisted %q %v %v", v, ok, err)
	}
	if err := again.EnsureFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, err := again.HasFTS(ctx); err != nil || !ok {
		t.Fatalf("repair after ui %v %v", ok, err)
	}
}

func TestClipIDsByBytesBoundsPage(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var msgs []Message
	for i := 0; i < 3; i++ {
		m := sample("e"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Minute))
		m.Subject = "clip token"
		m.Body = strings.Repeat("x", 40)
		m.HasBody = true
		msgs = append(msgs, m)
	}
	if err := db.UpsertMessages(ctx, msgs); err != nil {
		t.Fatal(err)
	}
	conn, err := db.sql.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ids, err := slimIDs(ctx, conn, "", nil, idCursor{}, 256)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Fatalf("slim %#v", ids)
	}
	clipped, err := clipIDsByBytesN(ctx, conn, ids, 50, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(clipped) == 0 || len(clipped) >= len(ids) {
		t.Fatalf("byte bound did not page %#v from %#v", clipped, ids)
	}
}

func TestIndexedUnicodePunctuationOracle(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	cafe := sample("cafe", at)
	cafe.Subject = "café crème"
	cafe.Body = "naïve résumé"
	cafe.HasBody = true
	cjk := sample("cjk", at.Add(time.Hour))
	cjk.Subject = "你好世界"
	cjk.Snippet = "東京チケット"
	punct := sample("punct", at.Add(2*time.Hour))
	punct.Subject = "it's-ok foo.bar"
	punct.Body = "path/win"
	punct.HasBody = true
	if err := db.UpsertMessages(ctx, []Message{cafe, cjk, punct}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	queries := []string{"café", `"café crème"`, "你好世界", "チケット", "it's-ok", "foo.bar", "naïve"}
	for _, q := range queries {
		idx := indexedIDs(t, db, q)
		lit := literalIDs(t, db, q)
		if strings.Join(idx, ",") != strings.Join(lit, ",") {
			t.Fatalf("%q indexed %#v literal %#v", q, idx, lit)
		}
		if len(idx) == 0 {
			t.Fatalf("%q expected hits", q)
		}
	}
}

func TestIndexStatesPendingRepairDisabled(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	msg := sample("m1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	msg.Subject = "statetoken"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	st, err := db.SearchIndexState(ctx)
	if err != nil || st == IndexStateRepair {
		t.Fatalf("fresh upsert %s %v", st, err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	st, err = db.SearchIndexState(ctx)
	if err != nil || st != IndexStateReady {
		t.Fatalf("ready %s %v", st, err)
	}
	if err := db.idx.setWatermark(ctx, 0); err != nil {
		t.Fatal(err)
	}
	st, err = db.SearchIndexState(ctx)
	if err != nil || st != IndexStatePending {
		t.Fatalf("pending %s %v", st, err)
	}
	if err := markFTSDirty(ctx, db.sql); err != nil {
		t.Fatal(err)
	}
	st, err = db.SearchIndexState(ctx)
	if err != nil || st != IndexStateRepair {
		t.Fatalf("repair %s %v", st, err)
	}
	if err := db.DisableSearchAccel(ctx); err != nil {
		t.Fatal(err)
	}
	st, err = db.SearchIndexState(ctx)
	if err != nil || st != IndexStateDisabled {
		t.Fatalf("disabled %s %v", st, err)
	}
}

func TestTransientIndexErrClassifiesBusyAndCancel(t *testing.T) {
	if !transientIndexErr(context.Canceled) || !transientIndexErr(context.DeadlineExceeded) || !transientIndexErr(errIndexUnavailable) {
		t.Fatal("context/unavailable must be transient")
	}
	if !transientIndexErr(errors.New("database is locked (5) (SQLITE_BUSY)")) {
		t.Fatal("busy must be transient")
	}
	if transientIndexErr(errors.New("no such table: fts")) {
		t.Fatal("schema failure is not transient")
	}
}

func TestFreshDBUsesCachedSearchText(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	if v, ok, err := db.GetState(ctx, stateFTSDirty); err != nil || (ok && v == ftsDirtyValue) {
		t.Fatalf("fresh dirty %q %v %v", v, ok, err)
	}
	msg := sample("m1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	msg.Subject = "helloxyz"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if st, err := db.SearchIndexState(ctx); err != nil || st == IndexStateRepair {
		t.Fatalf("fresh state %s %v", st, err)
	}
	if _, err := db.SQL().ExecContext(ctx, "UPDATE messages SET search_text = 'plantedxyz' WHERE id = 'm1'"); err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(t, db, "plantedxyz"); len(got) != 1 || got[0] != "m1" {
		t.Fatalf("cached miss %#v", got)
	}
	if got := searchIDs(t, db, "helloxyz"); len(got) != 0 {
		t.Fatalf("expression used %#v", got)
	}
}

func TestLabelOnlyCommitKeepsRevision(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	msg := sample("m1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	msg.Subject = "labeltoken"
	msg.Body = strings.Repeat("body ", 200)
	msg.HasBody = true
	msg.BodyFetched = true
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	rev := messageRev(t, db, "m1")
	msg.LabelIDs = []string{"INBOX", "STARRED"}
	msg.IsRead = true
	if err := db.CommitSyncPage(ctx, PageCommit{Messages: []Message{msg}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.HasFTS(ctx); err != nil || !ok {
		t.Fatalf("ready %v %v", ok, err)
	}
	if got := messageRev(t, db, "m1"); got != rev {
		t.Fatalf("revision %d -> %d", rev, got)
	}
	if got := indexedIDs(t, db, "labeltoken"); len(got) != 1 {
		t.Fatalf("hits %#v", got)
	}
}

func TestDateCommitEntersDelta(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	old := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	a := sample("a", old)
	a.Subject = "datetoken"
	b := sample("b", old.Add(time.Hour))
	b.Subject = "datetoken"
	if err := db.UpsertMessages(ctx, []Message{a, b}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	rev := messageRev(t, db, "a")
	a.InternalDate = old.Add(2 * time.Hour)
	if err := db.CommitSyncPage(ctx, PageCommit{Messages: []Message{a}}); err != nil {
		t.Fatal(err)
	}
	if got := messageRev(t, db, "a"); got <= rev {
		t.Fatalf("revision %d -> %d", rev, got)
	}
	if got := indexedIDs(t, db, "datetoken"); strings.Join(got, ",") != "a,b" {
		t.Fatalf("delta %#v", got)
	}
}

func TestClipNullCacheUsesCanonicalBytes(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var msgs []Message
	for i := 0; i < 3; i++ {
		m := sample("e"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Minute))
		m.Subject = "clip token"
		m.Body = strings.Repeat("x", 40)
		m.HasBody = true
		msgs = append(msgs, m)
	}
	if err := db.UpsertMessages(ctx, msgs); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, "UPDATE messages SET search_text = NULL"); err != nil {
		t.Fatal(err)
	}
	conn, err := db.sql.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ids, err := slimIDs(ctx, conn, "", nil, idCursor{}, 256)
	if err != nil {
		t.Fatal(err)
	}
	clipped, err := clipIDsByBytesN(ctx, conn, ids, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(clipped) == 0 || len(clipped) >= len(ids) {
		t.Fatalf("null cache did not page %#v from %#v", clipped, ids)
	}
}

func TestClipStaleNonNullUsesCanonicalBytes(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var msgs []Message
	for i := 0; i < 3; i++ {
		m := sample("s"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Minute))
		m.Subject = "clip token"
		m.Body = strings.Repeat("y", 40)
		m.HasBody = true
		msgs = append(msgs, m)
	}
	if err := db.UpsertMessages(ctx, msgs); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, "UPDATE messages SET search_text = 'tiny'"); err != nil {
		t.Fatal(err)
	}
	conn, err := db.sql.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ids, err := slimIDs(ctx, conn, "", nil, idCursor{}, 256)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := clipIDsByBytesN(ctx, conn, ids, 50, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cached) != len(ids) {
		t.Fatalf("cached path should not read body %#v", cached)
	}
	canon, err := clipIDsByBytesN(ctx, conn, ids, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(canon) == 0 || len(canon) >= len(ids) {
		t.Fatalf("stale non-null did not page %#v from %#v", canon, ids)
	}
}

func TestShortTermSkipsIndex(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	seedSearchMsgs(t, db)
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	parsed, err := search.Parse("zz")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.indexedSearch(ctx, ListFilter{Limit: 10, Query: "zz"}, parsed)
	if !errors.Is(err, errIndexSkip) {
		t.Fatalf("short term %v", err)
	}
}
