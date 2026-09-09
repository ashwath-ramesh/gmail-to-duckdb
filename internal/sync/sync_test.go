package sync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/gmail"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

type fakeAPI struct {
	profile      gmail.Profile
	labels       []store.Label
	listPages    [][]string
	listCalls    int
	afterList    func()
	historyPages []gmail.HistoryPage
	historyErr   error
	historyCalls int
	raw          map[string][]byte
	fullRaw      map[string][]byte
	fullErr      error
	gets         []string
}

func (f *fakeAPI) Profile(context.Context) (gmail.Profile, error) { return f.profile, nil }
func (f *fakeAPI) Labels(context.Context) ([]store.Label, error)  { return f.labels, nil }

func (f *fakeAPI) ListMessages(ctx context.Context, pageToken string) ([]string, string, error) {
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	i := f.listCalls
	f.listCalls++
	if f.afterList != nil {
		f.afterList()
	}
	if i >= len(f.listPages) {
		return nil, "", nil
	}
	next := ""
	if i+1 < len(f.listPages) {
		next = "p" + itoa(i+1)
	}
	return f.listPages[i], next, nil
}

func (f *fakeAPI) History(ctx context.Context, startID uint64, pageToken string) (gmail.HistoryPage, error) {
	if f.historyErr != nil {
		return gmail.HistoryPage{}, f.historyErr
	}
	i := f.historyCalls
	f.historyCalls++
	if i >= len(f.historyPages) {
		return gmail.HistoryPage{HistoryID: startID}, nil
	}
	return f.historyPages[i], nil
}

func (f *fakeAPI) BatchGet(_ context.Context, ids []string, format string) ([][]byte, error) {
	if format == "full" && f.fullErr != nil {
		return nil, f.fullErr
	}
	src := f.raw
	if format == "full" && f.fullRaw != nil {
		src = f.fullRaw
	}
	var out [][]byte
	for _, id := range ids {
		f.gets = append(f.gets, format+":"+id)
		if b, ok := src[id]; ok {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeAPI) Get(_ context.Context, id, format string) ([]byte, error) {
	src := f.raw
	if format == "full" && f.fullRaw != nil {
		src = f.fullRaw
	}
	b, ok := src[id]
	if !ok {
		return nil, errors.New("missing " + id)
	}
	return b, nil
}

func itoa(n int) string { return string(rune('0' + n)) }

func rawMsg(id, from, subject string, unread bool, body string) []byte {
	labels := []string{"INBOX"}
	if unread {
		labels = append(labels, "UNREAD")
	}
	m := map[string]any{
		"id":           id,
		"threadId":     "t-" + id,
		"labelIds":     labels,
		"snippet":      subject,
		"historyId":    "1",
		"internalDate": "1700000000000",
		"sizeEstimate": 10,
		"payload": map[string]any{
			"headers": []map[string]string{
				{"name": "From", "value": from},
				{"name": "To", "value": "you@example.com"},
				{"name": "Subject", "value": subject},
			},
		},
	}
	if body != "" {
		m["payload"] = map[string]any{
			"mimeType": "text/plain",
			"headers": []map[string]string{
				{"name": "From", "value": from},
				{"name": "To", "value": "you@example.com"},
				{"name": "Subject", "value": subject},
			},
			"body": map[string]any{"data": b64(body)},
		}
	}
	b, _ := json.Marshal(m)
	return b
}

func b64(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func openTest(t *testing.T) (*store.DB, *Runner, *fakeAPI) {
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
	api := &fakeAPI{
		profile: gmail.Profile{Email: "me@example.com", HistoryID: 100},
		labels:  []store.Label{{ID: "INBOX", Name: "Inbox", Type: "system"}},
		raw:     map[string][]byte{},
	}
	r := &Runner{DB: db, API: api, Log: func(string, ...any) {}}
	return db, r, api
}

func TestFirstSyncLoadsPages(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	api.listPages = [][]string{{"a"}, {"b"}}
	api.raw["a"] = rawMsg("a", "a@x.com", "A", true, "")
	api.raw["b"] = rawMsg("b", "b@x.com", "B", false, "")

	if err := r.Sync(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	a, err := db.GetMessage(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if a.Subject != "A" || a.HasBody {
		t.Fatalf("a: %+v", a)
	}
	if _, err := db.GetMessage(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	hid, ok, err := db.GetState(ctx, stateHistoryID)
	if err != nil || !ok || hid != "100" {
		t.Fatalf("history %q %v %v", hid, ok, err)
	}
}

func TestSyncBuildsFTS(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	api.listPages = [][]string{{"a"}}
	api.raw["a"] = rawMsg("a", "a@x.com", "Pineapple invoice", true, "")
	if err := r.Sync(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	hits, err := db.ListMessages(ctx, store.ListFilter{Limit: 10, Query: "pineapple"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "a" {
		t.Fatalf("fts after sync: %#v", hits)
	}
}

func TestFullMarksDeleted(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	old := store.Message{
		ID: "gone", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(),
		FromEmail: "z@z.com", Subject: "old",
	}
	if err := db.UpsertMessages(ctx, []store.Message{old}); err != nil {
		t.Fatal(err)
	}
	api.listPages = [][]string{{"keep"}}
	api.raw["keep"] = rawMsg("keep", "k@x.com", "K", false, "")

	if err := r.Sync(ctx, Options{Full: true}); err != nil {
		t.Fatal(err)
	}
	gone, err := db.GetMessage(ctx, "gone")
	if err != nil {
		t.Fatal(err)
	}
	if !gone.IsDeleted {
		t.Fatal("expected gone deleted")
	}
}

func TestHistoryAddDeleteLabel(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.SetState(ctx, stateHistoryID, "10"); err != nil {
		t.Fatal(err)
	}
	exist := store.Message{
		ID: "exist", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(),
		FromEmail: "e@x.com", Subject: "E", LabelIDs: []string{"UNREAD"}, IsRead: false,
	}
	if err := db.UpsertMessages(ctx, []store.Message{exist, {
		ID: "del", ThreadID: "t", InternalDate: time.Unix(1, 0).UTC(), FromEmail: "d@x.com",
	}}); err != nil {
		t.Fatal(err)
	}
	api.historyPages = []gmail.HistoryPage{{
		Added:   []string{"new"},
		Deleted: []string{"del"},
		LabelUpdates: []gmail.LabelUpdate{{
			ID: "exist", Labels: []string{"INBOX"},
		}},
		HistoryID: 20,
	}}
	api.raw["new"] = rawMsg("new", "n@x.com", "N", true, "")
	api.raw["exist"] = rawMsg("exist", "e@x.com", "E", false, "")

	if err := r.Sync(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetMessage(ctx, "new"); err != nil {
		t.Fatal(err)
	}
	del, err := db.GetMessage(ctx, "del")
	if err != nil {
		t.Fatal(err)
	}
	if !del.IsDeleted {
		t.Fatal("del")
	}
	got, err := db.GetMessage(ctx, "exist")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsRead || len(got.LabelIDs) != 1 || got.LabelIDs[0] != "INBOX" {
		t.Fatalf("labels %+v", got)
	}
	hid, _, _ := db.GetState(ctx, stateHistoryID)
	if hid != "20" {
		t.Fatalf("hid %s", hid)
	}
	okAt, ok, err := db.GetState(ctx, store.StateLastSyncOK)
	if err != nil || !ok || okAt == "" {
		t.Fatalf("last_sync_ok %q %v %v", okAt, ok, err)
	}
}

func TestStaleHistoryFallsBackToFull(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.SetState(ctx, stateHistoryID, "1"); err != nil {
		t.Fatal(err)
	}
	api.historyErr = gmail.ErrHistoryGone
	api.listPages = [][]string{{"a"}}
	api.raw["a"] = rawMsg("a", "a@x.com", "A", false, "")
	if err := r.Sync(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	if api.listCalls == 0 {
		t.Fatal("expected full list fallback")
	}
	if _, err := db.GetMessage(ctx, "a"); err != nil {
		t.Fatal(err)
	}
}

func TestBodies(t *testing.T) {
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
	api.historyPages = []gmail.HistoryPage{{HistoryID: 9}}
	api.fullRaw = map[string][]byte{
		"m": rawMsg("m", "a@x.com", "S", false, "hello body"),
	}
	if err := r.Sync(ctx, Options{Bodies: true}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMessage(ctx, "m")
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasBody || got.Body != "hello body" {
		t.Fatalf("body %+v", got)
	}
}

func TestHistoryIDSetBeforeBodiesFail(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	api.listPages = [][]string{{"a"}}
	api.raw["a"] = rawMsg("a", "a@x.com", "A", false, "")
	api.fullErr = errors.New("bodies fail")
	if err := r.Sync(ctx, Options{Bodies: true}); err == nil {
		t.Fatal("expected body error")
	}
	hid, ok, err := db.GetState(ctx, stateHistoryID)
	if err != nil || !ok || hid != "100" {
		t.Fatalf("history %q %v %v", hid, ok, err)
	}
	if _, ok, err := db.GetState(ctx, store.StateLastSyncOK); err != nil || ok {
		t.Fatal("last_sync_ok should be unset")
	}
	msg, ok, err := db.GetState(ctx, store.StateLastSyncError)
	if err != nil || !ok || msg == "" {
		t.Fatalf("last_sync_error %q %v %v", msg, ok, err)
	}
}

func TestFullClearsHistoryResume(t *testing.T) {
	ctx := context.Background()
	db, r, api := openTest(t)
	if err := db.SetState(ctx, stateHistoryPage, "stale"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, stateHistoryStart, "1"); err != nil {
		t.Fatal(err)
	}
	api.listPages = [][]string{{"a"}}
	api.raw["a"] = rawMsg("a", "a@x.com", "A", false, "")
	if err := r.Sync(ctx, Options{Full: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := db.GetState(ctx, stateHistoryPage); err != nil || ok {
		t.Fatalf("history page still set")
	}
	if _, ok, err := db.GetState(ctx, stateHistoryStart); err != nil || ok {
		t.Fatalf("history start still set")
	}
}

func TestListCheckpointOnCancel(t *testing.T) {
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
	err := r.Sync(ctx, Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if _, err := db.GetMessage(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	tok, ok, err := db.GetState(context.Background(), stateListPage)
	if err != nil || !ok || tok == "" {
		t.Fatalf("token %q %v %v", tok, ok, err)
	}
}
