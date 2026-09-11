package sync

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/gmail"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

func TestProfileMismatchAborts(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.SetState(ctx, store.StateProfileEmail, "other@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateHistoryID, "9"); err != nil {
		t.Fatal(err)
	}
	old := store.Message{ID: "m", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(), FromEmail: "a@x.com", Subject: "keep"}
	if err := db.UpsertMessages(ctx, []store.Message{old}); err != nil {
		t.Fatal(err)
	}
	api.historyPages = []gmail.HistoryPage{{Added: []string{"new"}, HistoryID: 20}}
	api.raw["new"] = rawMsg("new", "n@x.com", "N", false, "")
	if err := r.Sync(ctx, Options{}); !errors.Is(err, store.ErrAccountMismatch) {
		t.Fatalf("err %v", err)
	}
	if _, err := db.GetMessage(ctx, "new"); err == nil {
		t.Fatal("must not ingest after mismatch")
	}
	hid, _, _ := db.GetState(ctx, stateHistoryID)
	if hid != "9" {
		t.Fatalf("cursor %s", hid)
	}
	got, err := db.GetMessage(ctx, "m")
	if err != nil || got.Subject != "keep" {
		t.Fatalf("mail changed %+v %v", got, err)
	}
}

func TestEmptyProfileRejected(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.ClearState(ctx, store.StateProfileEmail); err != nil {
		t.Fatal(err)
	}
	api.profile.Email = ""
	if err := r.Sync(ctx, Options{}); !errors.Is(err, store.ErrEmptyProfile) {
		t.Fatalf("err %v", err)
	}
}

func TestPopulatedDBWithoutOwnerAborts(t *testing.T) {
	ctx := context.Background()
	db, r, _ := openTest(t)
	if err := db.ClearState(ctx, store.StateProfileEmail); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessages(ctx, []store.Message{{
		ID: "m", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(), FromEmail: "a@x.com",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Sync(ctx, Options{}); !errors.Is(err, store.ErrUnboundDatabase) {
		t.Fatalf("err %v", err)
	}
	if _, ok, _ := db.GetState(ctx, store.StateProfileEmail); ok {
		t.Fatal("must not bind")
	}
}

func TestMetadataMissingDoesNotAdvance(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	api.listPages = [][]string{{"a", "b"}}
	api.raw["a"] = rawMsg("a", "a@x.com", "A", false, "")
	if err := r.Sync(ctx, Options{}); err == nil {
		t.Fatal("expected unresolved metadata")
	}
	if _, err := db.GetMessage(ctx, "a"); err == nil {
		t.Fatal("atomic page must not commit")
	}
	if _, ok, _ := db.GetState(ctx, stateListPage); ok {
		t.Fatal("token must not advance")
	}
	if _, ok, _ := db.GetState(ctx, stateHistoryID); ok {
		t.Fatal("history must not advance")
	}
}

func TestMetadataRetryableDoesNotAdvance(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	api.listPages = [][]string{{"a"}}
	api.status = map[string]gmail.FetchStatus{"a": gmail.FetchRetryable}
	if err := r.Sync(ctx, Options{}); err == nil {
		t.Fatal("expected retryable")
	}
	if _, ok, _ := db.GetState(ctx, stateListPage); ok {
		t.Fatal("token")
	}
	if _, err := db.GetMessage(ctx, "a"); err == nil {
		t.Fatal("must not persist")
	}
}

func TestMetadata404MarksDeletedAndAdvances(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.UpsertMessages(ctx, []store.Message{{
		ID: "gone", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(), FromEmail: "z@z.com", Subject: "old",
	}}); err != nil {
		t.Fatal(err)
	}
	api.listPages = [][]string{{"gone"}}
	api.notFound = map[string]bool{"gone": true}
	if err := r.Sync(ctx, Options{Full: true}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "gone")
	if err != nil || !got.IsDeleted {
		t.Fatalf("404 %+v %v", got, err)
	}
	hid, ok, _ := db.GetState(ctx, stateHistoryID)
	if !ok || hid != "100" {
		t.Fatalf("history %q", hid)
	}
}

func TestFullAnchorPersistedAndReused(t *testing.T) {
	db, r, api := openTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	api.listPages = [][]string{{"a"}, {"b"}}
	api.raw["a"] = rawMsg("a", "a@x.com", "A", false, "")
	api.raw["b"] = rawMsg("b", "b@x.com", "B", false, "")
	api.afterList = func() {
		if api.listCalls >= 1 {
			cancel()
		}
	}
	if err := r.Sync(ctx, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	anchor, ok, err := db.GetState(context.Background(), stateFullStart)
	if err != nil || !ok || anchor != "100" {
		t.Fatalf("anchor %q %v %v", anchor, ok, err)
	}
	phase, ok, _ := db.GetState(context.Background(), stateFullPhase)
	if !ok || (phase != phaseList && phase != phaseListFull) {
		t.Fatalf("phase %q", phase)
	}
	api.profile.HistoryID = 200
	api.afterList = nil
	api.listCalls = 0
	if err := r.Sync(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if len(api.historyStarts) == 0 || api.historyStarts[0] != 100 {
		t.Fatalf("catchup used %v want 100", api.historyStarts)
	}
	if _, ok, _ := db.GetState(context.Background(), stateFullPhase); ok {
		t.Fatal("full phase should clear")
	}
}

func TestResumeFullWithoutFlag(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.SetState(ctx, stateHistoryID, "10"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateFullStart, "50"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateFullPhase, phaseList); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateListPage, "p1"); err != nil {
		t.Fatal(err)
	}
	api.listPages = [][]string{{"a"}, {"b"}}
	api.raw["a"] = rawMsg("a", "a@x.com", "A", false, "")
	api.raw["b"] = rawMsg("b", "b@x.com", "B", false, "")
	if err := r.Sync(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	if len(api.listTokens) == 0 || api.listTokens[0] != "p1" {
		t.Fatalf("must resume list token: %v", api.listTokens)
	}
	if api.historyCalls == 0 {
		t.Fatal("expected catchup")
	}
	if len(api.historyStarts) == 0 || api.historyStarts[0] != 50 {
		t.Fatalf("anchor %v", api.historyStarts)
	}
}

func TestAfterFullHistoryGoneIsError(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	api.listPages = [][]string{{"a"}}
	api.raw["a"] = rawMsg("a", "a@x.com", "A", false, "")
	api.historyErr = gmail.ErrHistoryGone
	if err := r.Sync(ctx, Options{}); err == nil || !errors.Is(err, gmail.ErrHistoryGone) {
		t.Fatalf("err %v", err)
	}
	if _, ok, _ := db.GetState(ctx, store.StateLastSyncOK); ok {
		t.Fatal("must not report success")
	}
	phase, ok, _ := db.GetState(ctx, stateFullPhase)
	if !ok || phase != phaseCatchup {
		t.Fatalf("phase %q %v", phase, ok)
	}
}

func TestExpiredListTokenRestartsOnce(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.SetState(ctx, stateFullStart, "50"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateFullPhase, phaseList); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateListPage, "expired"); err != nil {
		t.Fatal(err)
	}
	api.listPages = [][]string{{"a"}}
	api.raw["a"] = rawMsg("a", "a@x.com", "A", false, "")
	if err := r.Sync(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	var empty bool
	for _, tok := range api.listTokens {
		if tok == "" {
			empty = true
		}
	}
	if !empty {
		t.Fatalf("expected a fresh list: %v", api.listTokens)
	}
	if _, err := db.GetMessage(ctx, "a"); err != nil {
		t.Fatal(err)
	}
}

func TestRepeatedExpiryPreservesRestartableState(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.SetState(ctx, stateFullStart, "50"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateFullPhase, phaseList); err != nil {
		t.Fatal(err)
	}
	api.listPages = [][]string{{"a"}, {"b"}}
	api.raw["a"] = rawMsg("a", "a@x.com", "A", false, "")
	api.raw["b"] = rawMsg("b", "b@x.com", "B", false, "")
	api.expireToken = "p1"
	if err := r.Sync(ctx, Options{}); err == nil {
		t.Fatal("expected repeated expiry error")
	}
	if _, err := db.GetMessage(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	tok, ok, _ := db.GetState(ctx, stateListPage)
	if ok && tok != "" {
		t.Fatalf("stale token %q", tok)
	}
	anchor, ok, _ := db.GetState(ctx, stateFullStart)
	if !ok || anchor != "50" {
		t.Fatalf("anchor %q", anchor)
	}
	phase, ok, _ := db.GetState(ctx, stateFullPhase)
	if !ok || phase == "" {
		t.Fatal("must stay restartable")
	}
}

func TestLegacyResumeWithoutAnchorRestarts(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.SetState(ctx, stateHistoryID, "10"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateListPage, "stale"); err != nil {
		t.Fatal(err)
	}
	keep := store.Message{ID: "keep", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(), FromEmail: "k@x.com", Subject: "K"}
	if err := db.UpsertMessages(ctx, []store.Message{keep}); err != nil {
		t.Fatal(err)
	}
	api.listPages = [][]string{{"keep"}}
	api.raw["keep"] = rawMsg("keep", "k@x.com", "K", false, "")
	if err := r.Sync(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, tok := range api.listTokens {
		if tok == "stale" {
			t.Fatal("must not reuse untrustworthy token")
		}
	}
	got, err := db.GetMessage(ctx, "keep")
	if err != nil || got.Subject != "K" {
		t.Fatalf("preserved %+v %v", got, err)
	}
}

func TestHistoryGoneFallbackPreservesFullPhase(t *testing.T) {
	db, r, api := openTest(t)
	if err := db.SetState(context.Background(), stateHistoryID, "1"); err != nil {
		t.Fatal(err)
	}
	api.historyErr = gmail.ErrHistoryGone
	api.listPages = [][]string{{"a"}, {"b"}}
	api.raw["a"] = rawMsg("a", "a@x.com", "A", false, "")
	api.raw["b"] = rawMsg("b", "b@x.com", "B", false, "")
	ctx, cancel := context.WithCancel(context.Background())
	api.afterList = func() {
		if api.listCalls >= 1 {
			cancel()
		}
	}
	if err := r.Sync(ctx, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	phase, ok, _ := db.GetState(context.Background(), stateFullPhase)
	if !ok || phase == "" {
		t.Fatal("unfinished fallback must keep full_phase")
	}
	anchor, ok, _ := db.GetState(context.Background(), stateFullStart)
	if !ok || anchor != "100" {
		t.Fatalf("anchor %q", anchor)
	}
	api.historyErr = nil
	api.afterList = nil
	api.historyCalls = 0
	if err := r.Sync(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if api.listCalls < 2 {
		t.Fatalf("must resume list, listCalls=%d tokens=%v", api.listCalls, api.listTokens)
	}
}

func TestMixedAddDeleteDoesNotResurrect(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.SetState(ctx, stateHistoryID, "10"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessages(ctx, []store.Message{{
		ID: "gone", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(),
		FromEmail: "g@x.com", Subject: "G", IsDeleted: true,
	}}); err != nil {
		t.Fatal(err)
	}
	api.historyPages = []gmail.HistoryPage{{
		Added:     []string{"gone"},
		Deleted:   []string{"gone"},
		HistoryID: 20,
	}}
	api.notFound = map[string]bool{"gone": true}
	if err := r.Sync(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "gone")
	if err != nil || !got.IsDeleted {
		t.Fatalf("resurrected %+v %v", got, err)
	}
}

func TestFetchParsedPreservesAuthError(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	api.listPages = [][]string{{"a"}}
	api.status = map[string]gmail.FetchStatus{"a": gmail.FetchFatal}
	api.resultErr = map[string]error{"a": gmail.ErrAuth}
	err := r.Sync(ctx, Options{})
	if !errors.Is(err, gmail.ErrAuth) {
		t.Fatalf("dropped cause: %v", err)
	}
	if _, ok, _ := db.GetState(ctx, stateListPage); ok {
		t.Fatal("must not checkpoint")
	}
}

func TestFetchParsedCancelIsCanceled(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	api.listPages = [][]string{{"a"}}
	api.raw["a"] = rawMsg("a", "a@x.com", "A", false, "")
	api.batchWithErr = context.Canceled
	err := r.Sync(ctx, Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if _, err := db.GetMessage(ctx, "a"); err == nil {
		t.Fatal("metadata must not checkpoint")
	}
}

func TestBodiesPartialPlusTransportPersists(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.UpsertMessages(ctx, []store.Message{
		{ID: "ok", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(), FromEmail: "a@x.com", Subject: "S"},
		{ID: "miss", ThreadID: "t", InternalDate: time.Unix(2, 0).UTC(), FromEmail: "a@x.com", Subject: "M"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateHistoryID, "9"); err != nil {
		t.Fatal(err)
	}
	transport := errors.New("transport down")
	api.fullRaw = map[string][]byte{"ok": rawMsg("ok", "a@x.com", "S", false, "hello body")}
	api.batchWithErr = transport
	err := r.Sync(ctx, Options{Bodies: true})
	if err == nil || !errors.Is(err, transport) {
		t.Fatalf("err %v", err)
	}
	got, err := db.GetMessage(ctx, "ok")
	if err != nil || !got.BodyFetched || got.Body != "hello body" {
		t.Fatalf("body %+v %v", got, err)
	}
	pending, err := db.GetMessage(ctx, "miss")
	if err != nil || pending.BodyFetched {
		t.Fatalf("miss %+v %v", pending, err)
	}
}

func TestMetadataPartialPlusTransportDoesNotCheckpoint(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	api.listPages = [][]string{{"a", "b"}}
	api.raw["a"] = rawMsg("a", "a@x.com", "A", false, "")
	api.batchWithErr = errors.New("transport down")
	if err := r.Sync(ctx, Options{}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := db.GetMessage(ctx, "a"); err == nil {
		t.Fatal("must not persist incomplete metadata")
	}
	if _, ok, _ := db.GetState(ctx, stateListPage); ok {
		t.Fatal("must not advance token")
	}
}

func TestFinishStateWriteFailureIsNotSuccess(t *testing.T) {
	db, r, _ := openTest(t)
	_ = db.Close()
	if err := r.finish(nil); err == nil {
		t.Fatal("must not report success when last_sync_ok cannot be written")
	}
}

func TestUnknownFullPhaseDoesNotSucceedSilently(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.SetState(ctx, stateHistoryID, "10"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateFullStart, "50"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateFullPhase, "bogus"); err != nil {
		t.Fatal(err)
	}
	api.listPages = [][]string{{"a"}}
	api.raw["a"] = rawMsg("a", "a@x.com", "A", false, "")
	err := r.Sync(ctx, Options{})
	okAt, ok, _ := db.GetState(ctx, store.StateLastSyncOK)
	if err == nil && api.listCalls == 0 {
		t.Fatal("unknown phase must not silently succeed")
	}
	if ok && api.listCalls == 0 {
		t.Fatalf("last_sync_ok without work: %q", okAt)
	}
}

func TestMissingCatchupAnchorDoesNotSucceed(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.SetState(ctx, stateHistoryID, "10"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateFullPhase, phaseCatchup); err != nil {
		t.Fatal(err)
	}
	api.listPages = [][]string{{"a"}}
	api.raw["a"] = rawMsg("a", "a@x.com", "A", false, "")
	if err := r.Sync(ctx, Options{}); err == nil {
		if api.listCalls == 0 {
			t.Fatal("missing catchup anchor must not succeed")
		}
	}
	if _, ok, _ := db.GetState(ctx, store.StateLastSyncOK); ok && api.listCalls == 0 {
		t.Fatal("last_sync_ok without work")
	}
}

func TestFinalPageCancelPersistsAndReturnsCanceled(t *testing.T) {
	db, r, api := openTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	api.listPages = [][]string{{"a"}}
	api.raw["a"] = rawMsg("a", "a@x.com", "A", false, "")
	api.afterList = func() { cancel() }
	err := r.Sync(ctx, Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if _, err := db.GetMessage(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.GetState(context.Background(), store.StateLastSyncOK); ok {
		t.Fatal("last_sync_ok")
	}
}

func TestFetchOnDemandRejectsEmptyID(t *testing.T) {
	ctx := context.Background()
	db, _, api := openTest(t)
	api.fullRaw = map[string][]byte{"m": []byte(`{"threadId":"t"}`)}
	err := FetchOnDemand(ctx, db, api, "m")
	if err == nil {
		t.Fatal("empty payload id must fail")
	}
}

func TestFetchParsedRejectsEmptyID(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	api.listPages = [][]string{{"a"}}
	api.raw["a"] = []byte(`{}`)
	if err := r.Sync(ctx, Options{}); err == nil {
		t.Fatal("empty id must be unresolved")
	}
	if _, err := db.GetMessage(ctx, "a"); err == nil {
		t.Fatal("must not persist")
	}
}

func TestUnsafeV2InterruptedFullRestartsList(t *testing.T) {
	ctx := context.Background()
	path := writeV2InterruptedFull(t)
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	phase, ok, err := db.GetState(ctx, "full_phase")
	if err != nil || !ok || phase != phaseListFull {
		t.Fatalf("want list_full restart intent, got %q %v %v", phase, ok, err)
	}
	if _, ok, _ := db.GetState(ctx, "full_start_history_id"); ok {
		t.Fatal("unsafe anchor must be cleared")
	}
	api := &fakeAPI{
		profile:   gmail.Profile{Email: "me@example.com", HistoryID: 100},
		labels:    []store.Label{{ID: "INBOX", Name: "Inbox", Type: "system"}},
		listPages: [][]string{{"fresh"}},
		raw:       map[string][]byte{"fresh": rawMsg("fresh", "a@x.com", "F", false, "")},
	}
	r := &Runner{DB: db, API: api, Log: func(string, ...any) {}}
	if err := r.Sync(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	if api.listCalls == 0 {
		t.Fatalf("default sync must list, not only history: hist=%#v", api.historyStarts)
	}
	if len(api.listTokens) == 0 || api.listTokens[0] != "" {
		t.Fatalf("fresh list token %#v", api.listTokens)
	}
	if _, err := db.GetMessage(ctx, "fresh"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetMessage(ctx, "m"); err != nil {
		t.Fatal(err)
	}
}

func TestFinalHistoryPageCancelPersistsAndReturnsCanceled(t *testing.T) {
	db, r, api := openTest(t)
	ctx := context.Background()
	if err := db.SetState(ctx, stateHistoryID, "9"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, store.StateLastSyncOK, "2024-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	api.historyPages = []gmail.HistoryPage{{Added: []string{"n"}, HistoryID: 20}}
	api.raw["n"] = rawMsg("n", "a@x.com", "N", false, "")
	run, cancel := context.WithCancel(context.Background())
	api.afterHistory = func() { cancel() }
	err := r.Sync(run, Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if _, err := db.GetMessage(context.Background(), "n"); err != nil {
		t.Fatal(err)
	}
	hid, ok, err := db.GetState(context.Background(), stateHistoryID)
	if err != nil || !ok || hid != "20" {
		t.Fatalf("saved cursor %q %v %v", hid, ok, err)
	}
	okAt, ok, err := db.GetState(context.Background(), store.StateLastSyncOK)
	if err != nil || !ok || okAt != "2024-01-01T00:00:00Z" {
		t.Fatalf("last_sync_ok changed %q %v %v", okAt, ok, err)
	}
	ready, err := db.HasFTS(context.Background())
	if err != nil || ready {
		t.Fatalf("canceled sync must not rebuild fts %v %v", ready, err)
	}
}

func TestCanceledBodySyncDoesNotRebuildFTS(t *testing.T) {
	db, r, api := openTest(t)
	ctx := context.Background()
	if err := db.UpsertMessages(ctx, []store.Message{{
		ID: "m", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(), FromEmail: "a@x.com", Subject: "S",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateHistoryID, "9"); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	api.historyPages = []gmail.HistoryPage{{HistoryID: 9}}
	api.fullRaw = map[string][]byte{"m": rawMsg("m", "a@x.com", "S", false, "saved-body")}
	run, cancel := context.WithCancel(context.Background())
	api.afterBatch = func() { cancel() }
	err := r.Sync(run, Options{Bodies: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	got, err := db.GetMessage(context.Background(), "m")
	if err != nil {
		t.Fatal(err)
	}
	if !got.BodyFetched || got.Body != "saved-body" {
		t.Fatalf("body persist %+v", got)
	}
	ready, err := db.HasFTS(context.Background())
	if err != nil || ready {
		t.Fatalf("canceled sync must not start fts rebuild %v %v", ready, err)
	}
}

func TestFetchOnDemandCancelAfterBodyPersists(t *testing.T) {
	db, _, api := openTest(t)
	ctx := context.Background()
	if err := db.UpsertMessages(ctx, []store.Message{{
		ID: "m", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(), FromEmail: "a@x.com", Subject: "S",
	}}); err != nil {
		t.Fatal(err)
	}
	api.fullRaw = map[string][]byte{"m": rawMsg("m", "a@x.com", "S", false, "saved-body")}
	run, cancel := context.WithCancel(context.Background())
	api.afterGet = func() { cancel() }
	err := FetchOnDemand(run, db, api, "m")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	got, err := db.GetMessage(ctx, "m")
	if err != nil {
		t.Fatal(err)
	}
	if !got.BodyFetched || got.Body != "saved-body" {
		t.Fatalf("must persist body %+v", got)
	}
}

func TestFinalHistoryPageMissingIDDoesNotAdvance(t *testing.T) {
	db, r, api := openTest(t)
	ctx := context.Background()
	if err := db.SetState(ctx, stateHistoryID, "9"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateFullPhase, phaseCatchup); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateFullStart, "50"); err != nil {
		t.Fatal(err)
	}
	api.historyPages = []gmail.HistoryPage{{Added: []string{"n"}}}
	api.raw["n"] = rawMsg("n", "a@x.com", "N", false, "")
	if err := r.Sync(ctx, Options{}); err == nil {
		t.Fatal("malformed final history page must error")
	}
	hid, ok, err := db.GetState(ctx, stateHistoryID)
	if err != nil || !ok || hid != "9" {
		t.Fatalf("history advanced %q %v %v", hid, ok, err)
	}
	phase, ok, err := db.GetState(ctx, stateFullPhase)
	if err != nil || !ok || phase != phaseCatchup {
		t.Fatalf("catchup cleared %q %v %v", phase, ok, err)
	}
	if _, err := db.GetMessage(ctx, "n"); err == nil {
		t.Fatal("must not commit malformed final page")
	}
}

func writeV2InterruptedFull(t *testing.T) string {
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
	path := filepath.Join(dir, "v2.duckdb")
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
  ('profile_email', 'me@example.com'),
  ('search_text_v1', '1'),
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

func TestDirtyNoopSyncRepairsFTS(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.UpsertMessages(ctx, []store.Message{{
		ID: "m", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(), FromEmail: "a@x.com", Subject: "oldxyz",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateHistoryID, "9"); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBody(ctx, "m", "later"); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.HasFTS(ctx); err != nil || ok {
		t.Fatalf("dirty before noop %v %v", ok, err)
	}
	api.historyPages = []gmail.HistoryPage{{HistoryID: 9}}
	if err := r.Sync(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	if api.listCalls != 0 {
		t.Fatalf("noop incremental listed: %d", api.listCalls)
	}
	ok, err := db.HasFTS(ctx)
	if err != nil || ok {
		t.Fatalf("noop sync must not rebuild index %v %v", ok, err)
	}
	hits, err := db.ListMessages(ctx, store.ListFilter{Limit: 10, Query: "later"})
	if err != nil || len(hits) != 1 || hits[0].ID != "m" {
		t.Fatalf("literal membership %#v %v", hits, err)
	}
}

func TestFetchOnDemandBindsAccount(t *testing.T) {
	ctx := context.Background()
	db, _, api := openTest(t)
	if err := db.SetState(ctx, store.StateProfileEmail, "other@example.com"); err != nil {
		t.Fatal(err)
	}
	api.fullRaw = map[string][]byte{"m": rawMsg("m", "a@x.com", "S", false, "body")}
	err := FetchOnDemand(ctx, db, api, "m")
	if !errors.Is(err, store.ErrAccountMismatch) {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(strings.Join(api.gets, ","), "full:m") {
		t.Fatalf("must not fetch after mismatch: %#v", api.gets)
	}
}
