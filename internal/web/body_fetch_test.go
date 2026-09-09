package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/gmail"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	mailsync "github.com/ashwath-ramesh/gmail-to-duckdb/internal/sync"
)

func TestGetAndOnDemandBodyFlags(t *testing.T) {
	s, db := testServer(t)
	ctx := context.Background()
	if err := db.UpsertMessages(ctx, []store.Message{
		{ID: "full", ThreadID: "t", InternalDate: time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC), FromEmail: "b@x.com", Subject: "Full", Body: "hello", HasBody: true, BodyFetched: true},
		{ID: "empty", ThreadID: "t", InternalDate: time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC), FromEmail: "c@x.com", Subject: "Empty"},
	}); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	pending := getMessageJSON(t, h, "/api/messages/m1?body=1")
	if pending["has_body"] != false || pending["body_fetched"] != false {
		t.Fatalf("pending %#v", pending)
	}

	full := getMessageJSON(t, h, "/api/messages/full?body=1")
	if full["has_body"] != true || full["body_fetched"] != true || full["body"] != "hello" {
		t.Fatalf("full %#v", full)
	}

	s.FetchBody = func(ctx context.Context, id string) (string, error) {
		return "", nil
	}
	w := req(t, h, http.MethodPost, "/api/messages/empty/body")
	if w.Code != 200 {
		t.Fatalf("empty post %d %s", w.Code, w.Body.String())
	}
	got, err := db.GetMessage(ctx, "empty")
	if err != nil {
		t.Fatal(err)
	}
	if got.HasBody || !got.BodyFetched {
		t.Fatalf("on-demand empty: %+v", got)
	}
	empty := getMessageJSON(t, h, "/api/messages/empty?body=1")
	if empty["has_body"] != false || empty["body_fetched"] != true {
		t.Fatalf("empty json %#v", empty)
	}

	s.FetchBody = func(ctx context.Context, id string) (string, error) {
		return "fetched body", nil
	}
	w = req(t, h, http.MethodPost, "/api/messages/m1/body")
	if w.Code != 200 {
		t.Fatalf("body post %d %s", w.Code, w.Body.String())
	}
	filled := getMessageJSON(t, h, "/api/messages/m1?body=1")
	if filled["has_body"] != true || filled["body_fetched"] != true || filled["body"] != "fetched body" {
		t.Fatalf("filled %#v", filled)
	}

	st := req(t, h, http.MethodGet, "/api/status")
	var env query.Envelope
	if err := json.Unmarshal(st.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.BodyCoverage.WithBody != 2 || env.BodyCoverage.Total != 3 {
		t.Fatalf("coverage %+v", env.BodyCoverage)
	}
}

func TestSyncCancelThenRunAgain(t *testing.T) {
	s, db := testServer(t)
	ctx := context.Background()
	if err := db.SetState(ctx, "history_id", "9"); err != nil {
		t.Fatal(err)
	}
	api := &blockFullAPI{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		full: map[string][]byte{
			"m1": []byte(`{"id":"m1","threadId":"t1","labelIds":["INBOX"],"snippet":"sn","historyId":"1","internalDate":"1700000000000","payload":{"mimeType":"text/plain","headers":[{"name":"From","value":"a@x.com"},{"name":"Subject","value":"Hello"}],"body":{"data":"ZmV0Y2hlZA=="}}}`),
		},
	}
	t.Cleanup(func() {
		select {
		case <-api.release:
		default:
			close(api.release)
		}
	})
	s.Sync = (&mailsync.Runner{DB: db, API: api, Log: func(string, ...any) {}}).Sync

	syncCtx, stop := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.WaitSync(syncCtx, mailsync.Options{Bodies: true}) }()
	select {
	case <-api.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("full BatchGet did not start")
	}
	stop()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first sync: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server sync slot held")
	}
	if err := s.WaitSync(context.Background(), mailsync.Options{Bodies: true}); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	got, err := db.GetMessage(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.BodyFetched || got.Body != "fetched" {
		t.Fatalf("retry: %+v", got)
	}
}

type blockFullAPI struct {
	entered chan struct{}
	release chan struct{}
	n       int
	full    map[string][]byte
}

func (a *blockFullAPI) Profile(context.Context) (gmail.Profile, error) {
	return gmail.Profile{Email: "me@example.com", HistoryID: 9}, nil
}
func (a *blockFullAPI) Labels(context.Context) ([]store.Label, error) { return nil, nil }
func (a *blockFullAPI) ListMessages(context.Context, string) ([]string, string, error) {
	return nil, "", nil
}
func (a *blockFullAPI) History(context.Context, uint64, string) (gmail.HistoryPage, error) {
	return gmail.HistoryPage{HistoryID: 9}, nil
}
func (a *blockFullAPI) Get(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("unused")
}
func (a *blockFullAPI) BatchGet(ctx context.Context, ids []string, format string) ([][]byte, error) {
	if format != "full" {
		return nil, nil
	}
	a.n++
	if a.n == 1 {
		select {
		case <-a.entered:
		default:
			close(a.entered)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-a.release:
			return nil, errors.New("released")
		}
	}
	var out [][]byte
	for _, id := range ids {
		if b, ok := a.full[id]; ok {
			out = append(out, b)
		}
	}
	return out, nil
}

func getMessageJSON(t *testing.T, h http.Handler, path string) map[string]any {
	t.Helper()
	w := req(t, h, http.MethodGet, path)
	if w.Code != 200 {
		t.Fatalf("%s %d %s", path, w.Code, w.Body.String())
	}
	var env struct {
		Message map[string]any `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Message == nil {
		t.Fatalf("no message %s", w.Body.String())
	}
	return env.Message
}
