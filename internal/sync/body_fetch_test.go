package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/gmail"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

func TestBodiesEmptyAndAttachmentFetchedOnce(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.UpsertMessages(ctx, []store.Message{
		{ID: "empty", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(), FromEmail: "a@x.com", Subject: "E"},
		{ID: "att", ThreadID: "t", InternalDate: time.Unix(2, 0).UTC(), FromEmail: "a@x.com", Subject: "A"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateHistoryID, "9"); err != nil {
		t.Fatal(err)
	}
	api.maxFull = 8
	api.fullRaw = map[string][]byte{
		"empty": rawMsg("empty", "a@x.com", "E", false, ""),
		"att":   rawAttachmentOnly("att", "a@x.com", "A"),
	}

	if err := r.Sync(ctx, Options{Bodies: true}); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	empty, err := db.GetMessage(ctx, "empty")
	if err != nil {
		t.Fatal(err)
	}
	if empty.HasBody || !empty.BodyFetched {
		t.Fatalf("empty: %+v", empty)
	}
	att, err := db.GetMessage(ctx, "att")
	if err != nil {
		t.Fatal(err)
	}
	if att.HasBody || !att.BodyFetched {
		t.Fatalf("attachment: %+v", att)
	}
	if countGets(api.gets, "full:empty") != 1 || countGets(api.gets, "full:att") != 1 {
		t.Fatalf("first sync must fetch each id once: %#v", api.gets)
	}

	if err := r.Sync(ctx, Options{Bodies: true}); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if countGets(api.gets, "full:empty") != 1 || countGets(api.gets, "full:att") != 1 {
		t.Fatalf("second sync must not refetch: %#v", api.gets)
	}
	c, err := db.Coverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.WithBody != 0 || c.Total != 2 {
		t.Fatalf("coverage %+v", c)
	}
}

func TestBodiesMissingFirstIDsDoNotStarveLater(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	msgs := make([]store.Message, 0, 51)
	for i := 0; i < 51; i++ {
		id := fmt.Sprintf("a%03d", i)
		msgs = append(msgs, store.Message{
			ID: id, ThreadID: "t", InternalDate: time.Unix(int64(i+1), 0).UTC(),
			FromEmail: "a@x.com", Subject: id,
		})
	}
	if err := db.UpsertMessages(ctx, msgs); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateHistoryID, "9"); err != nil {
		t.Fatal(err)
	}
	firstPage, err := db.IDsNeedingFetch(ctx, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	inFirst := map[string]bool{}
	for _, id := range firstPage {
		inFirst[id] = true
	}
	later := ""
	for i := 0; i < 51; i++ {
		id := fmt.Sprintf("a%03d", i)
		if !inFirst[id] {
			later = id
			break
		}
	}
	if later == "" {
		t.Fatal("need an id outside the first page")
	}
	api.maxFull = 8
	api.fullRaw = map[string][]byte{
		later: rawMsg(later, "a@x.com", later, false, "later body"),
	}

	err = r.Sync(ctx, Options{Bodies: true})
	if err == nil {
		t.Fatal("expected incomplete body fetch")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("sync looped until cancel")
	}
	got, err := db.GetMessage(ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasBody || !got.BodyFetched || got.Body != "later body" {
		t.Fatalf("later id %s starved: %+v", later, got)
	}
	if api.fullBatches < 2 {
		t.Fatalf("expected a second page, batches=%d gets=%#v", api.fullBatches, api.gets)
	}
}

func TestBodiesFailedPendingRetriedNextSync(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.UpsertMessages(ctx, []store.Message{{
		ID: "m", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(), FromEmail: "a@x.com", Subject: "S",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateHistoryID, "9"); err != nil {
		t.Fatal(err)
	}
	api.maxFull = 8
	api.fullRaw = map[string][]byte{}

	if err := r.Sync(ctx, Options{Bodies: true}); err == nil {
		t.Fatal("expected incomplete first sync")
	}
	pending, err := db.GetMessage(ctx, "m")
	if err != nil {
		t.Fatal(err)
	}
	if pending.BodyFetched {
		t.Fatalf("missing response must stay pending: %+v", pending)
	}
	first := countGets(api.gets, "full:m")
	if first != 1 {
		t.Fatalf("first attempt %#v", api.gets)
	}

	api.fullRaw["m"] = rawMsg("m", "a@x.com", "S", false, "hello body")
	if err := r.Sync(ctx, Options{Bodies: true}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "m")
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasBody || !got.BodyFetched || got.Body != "hello body" {
		t.Fatalf("retry: %+v", got)
	}
	if countGets(api.gets, "full:m") != 2 {
		t.Fatalf("expected one retry, got %#v", api.gets)
	}
}

func TestBodiesUnparseableStaysPending(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.UpsertMessages(ctx, []store.Message{{
		ID: "bad", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(), FromEmail: "a@x.com", Subject: "B",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateHistoryID, "9"); err != nil {
		t.Fatal(err)
	}
	api.maxFull = 8
	api.fullRaw = map[string][]byte{"bad": []byte("{")}
	if err := r.Sync(ctx, Options{Bodies: true}); err == nil {
		t.Fatal("expected incomplete")
	}
	got, err := db.GetMessage(ctx, "bad")
	if err != nil {
		t.Fatal(err)
	}
	if got.BodyFetched {
		t.Fatalf("unparseable must stay pending: %+v", got)
	}
	if countGets(api.gets, "full:bad") != 1 {
		t.Fatalf("unparseable retried in one sync: %#v", api.gets)
	}
}

func TestBodiesPartialRebuildsFTS(t *testing.T) {
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
	api.maxFull = 8
	api.fullRaw = map[string][]byte{
		"ok": rawMsg("ok", "a@x.com", "S", false, "unique pineapple body"),
	}
	if err := r.Sync(ctx, Options{Bodies: true}); err == nil {
		t.Fatal("expected incomplete")
	}
	fts, err := db.HasFTS(ctx)
	if err != nil || !fts {
		t.Fatalf("partial body writes must rebuild fts: %v %v", fts, err)
	}
	okAt, ok, err := db.GetState(ctx, store.StateLastSyncOK)
	if err != nil || ok {
		t.Fatalf("last_sync_ok must stay unset: %q %v %v", okAt, ok, err)
	}
	msg, ok, err := db.GetState(ctx, store.StateLastSyncError)
	if err != nil || !ok || msg == "" {
		t.Fatalf("last_sync_error %q %v %v", msg, ok, err)
	}
	hits, err := db.ListMessages(ctx, store.ListFilter{Limit: 10, Query: "pineapple"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "ok" {
		t.Fatalf("fts after partial: %#v", hits)
	}
}

func TestMetadataRefreshPreservesFetched(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.UpsertMessages(ctx, []store.Message{{
		ID: "m", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(), FromEmail: "a@x.com", Subject: "S",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateHistoryID, "9"); err != nil {
		t.Fatal(err)
	}
	api.maxFull = 8
	api.fullRaw = map[string][]byte{
		"m": rawMsg("m", "a@x.com", "S", false, "kept body"),
	}
	if err := r.Sync(ctx, Options{Bodies: true}); err != nil {
		t.Fatal(err)
	}
	api.historyCalls = 0
	api.historyPages = []gmail.HistoryPage{{
		LabelUpdates: []gmail.LabelUpdate{{ID: "m", Labels: []string{"INBOX"}}},
		HistoryID:    11,
	}}
	api.raw["m"] = rawMsg("m", "a@x.com", "New subject", false, "")
	if err := r.Sync(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "m")
	if err != nil {
		t.Fatal(err)
	}
	if !got.BodyFetched || !got.HasBody || got.Body != "kept body" {
		t.Fatalf("metadata lost fetch: %+v", got)
	}
}

func TestBodiesIgnoreExtraAndDuplicateIDs(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.UpsertMessages(ctx, []store.Message{
		{ID: "a", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(), FromEmail: "a@x.com", Subject: "A"},
		{ID: "b", ThreadID: "t", InternalDate: time.Unix(2, 0).UTC(), FromEmail: "a@x.com", Subject: "B"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateHistoryID, "9"); err != nil {
		t.Fatal(err)
	}
	api.fullRaw = map[string][]byte{
		"a": rawMsg("a", "a@x.com", "A", false, "body-a"),
		"b": rawMsg("b", "a@x.com", "B", false, "body-b"),
	}
	api.extraFull = [][]byte{
		rawMsg("a", "a@x.com", "A", false, "dup-a"),
		rawMsg("zzz", "z@x.com", "Z", false, "sneak"),
	}
	if err := r.Sync(ctx, Options{Bodies: true}); err != nil {
		t.Fatal(err)
	}
	a, err := db.GetMessage(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if !a.BodyFetched || a.Body != "body-a" {
		t.Fatalf("requested a: %+v", a)
	}
	b, err := db.GetMessage(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if !b.BodyFetched || b.Body != "body-b" {
		t.Fatalf("requested b: %+v", b)
	}
	if _, err := db.GetMessage(ctx, "zzz"); err == nil {
		t.Fatal("unexpected id must not persist")
	}
}

func TestBodiesCancelDuringBatchGet(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.UpsertMessages(ctx, []store.Message{{
		ID: "m", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(), FromEmail: "a@x.com", Subject: "S",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateHistoryID, "9"); err != nil {
		t.Fatal(err)
	}
	api.blockFull = true
	api.enteredFull = make(chan struct{})
	api.releaseFull = make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-api.releaseFull:
		default:
			close(api.releaseFull)
		}
	})

	syncCtx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- r.Sync(syncCtx, Options{Bodies: true}) }()
	select {
	case <-api.enteredFull:
	case <-time.After(3 * time.Second):
		t.Fatal("full BatchGet did not start")
	}
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want cancel, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sync held after cancel")
	}

	pending, err := db.GetMessage(ctx, "m")
	if err != nil {
		t.Fatal(err)
	}
	if pending.BodyFetched {
		t.Fatalf("canceled fetch must stay pending: %+v", pending)
	}

	api.blockFull = false
	api.fullRaw = map[string][]byte{
		"m": rawMsg("m", "a@x.com", "S", false, "after cancel"),
	}
	if err := r.Sync(ctx, Options{Bodies: true}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "m")
	if err != nil {
		t.Fatal(err)
	}
	if !got.BodyFetched || got.Body != "after cancel" {
		t.Fatalf("retry after cancel: %+v", got)
	}
}

func TestBodiesIncompleteThenTransportKeepsBoth(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	msgs := make([]store.Message, 0, 51)
	for i := 0; i < 51; i++ {
		id := fmt.Sprintf("a%03d", i)
		msgs = append(msgs, store.Message{
			ID: id, ThreadID: "t", InternalDate: time.Unix(int64(i+1), 0).UTC(),
			FromEmail: "a@x.com", Subject: id,
		})
	}
	if err := db.UpsertMessages(ctx, msgs); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateHistoryID, "9"); err != nil {
		t.Fatal(err)
	}
	transport := errors.New("transport down")
	api.fullErr = transport
	api.failAfter = 1
	api.fullRaw = map[string][]byte{
		"a000": rawMsg("a000", "a@x.com", "a000", false, "unique pineapple body"),
	}
	err := r.Sync(ctx, Options{Bodies: true})
	if err == nil {
		t.Fatal("expected joined error")
	}
	if !errors.Is(err, errBodyIncomplete) || !errors.Is(err, transport) {
		t.Fatalf("want incomplete+transport, got %v", err)
	}
	fts, err := db.HasFTS(ctx)
	if err != nil || !fts {
		t.Fatalf("successful writes must rebuild fts: %v %v", fts, err)
	}
	hits, err := db.ListMessages(ctx, store.ListFilter{Limit: 10, Query: "pineapple"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "a000" {
		t.Fatalf("fts %#v", hits)
	}
}

func rawAttachmentOnly(id, from, subject string) []byte {
	b, err := json.Marshal(map[string]any{
		"id":           id,
		"threadId":     "t-" + id,
		"labelIds":     []string{"INBOX"},
		"snippet":      subject,
		"historyId":    "1",
		"internalDate": "1700000000000",
		"sizeEstimate": 10,
		"payload": map[string]any{
			"mimeType": "multipart/mixed",
			"headers": []map[string]string{
				{"name": "From", "value": from},
				{"name": "To", "value": "you@example.com"},
				{"name": "Subject", "value": subject},
			},
			"parts": []map[string]any{{
				"mimeType": "application/pdf",
				"filename": "a.pdf",
				"body":     map[string]any{"attachmentId": "att", "size": 12},
			}},
		},
	})
	if err != nil {
		panic(err)
	}
	return b
}

func countGets(gets []string, prefix string) int {
	n := 0
	for _, g := range gets {
		if len(g) >= len(prefix) && g[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}
