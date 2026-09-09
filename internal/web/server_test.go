package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	mailsync "github.com/ashwath-ramesh/gmail-to-duckdb/internal/sync"
)

func testServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "mail.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := db.UpsertMessages(ctx, []store.Message{{
		ID: "m1", ThreadID: "t1", InternalDate: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
		FromEmail: "a@x.com", Subject: "Hello", Snippet: "sn", LabelIDs: []string{"INBOX"},
	}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{DB: db, Token: "test", FetchBody: func(ctx context.Context, id string) (string, error) {
		return "fetched body", nil
	}}
	return s, db
}

func req(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Token", "test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestIndexAndList(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	w := req(t, h, http.MethodGet, "/")
	if w.Code != 200 || len(w.Body.Bytes()) == 0 {
		t.Fatalf("index %d", w.Code)
	}
	html := w.Body.String()
	if !strings.Contains(html, "Search mail") || strings.Contains(html, "Label id") {
		t.Fatalf("ui %s", html)
	}
	w = req(t, h, http.MethodGet, "/api/messages")
	if w.Code != 200 {
		t.Fatalf("list %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 || out.Messages[0]["id"] != "m1" {
		t.Fatalf("%s", w.Body.String())
	}
}

func TestSecurityHeaders(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	w := req(t, h, http.MethodGet, "/")
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "img-src 'self'") {
		t.Fatalf("csp %q", csp)
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("referrer %q", w.Header().Get("Referrer-Policy"))
	}
	html := w.Body.String()
	if !strings.Contains(html, `content="no-referrer"`) {
		t.Fatalf("missing referrer meta: %s", html)
	}
}

func TestSearchQuery(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	w := req(t, h, http.MethodGet, "/api/messages?q=from:a@x.com")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var out struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 || out.Messages[0]["id"] != "m1" {
		t.Fatalf("%s", w.Body.String())
	}
	w = req(t, h, http.MethodGet, "/api/messages?q=from:nobody@x.com")
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 0 {
		t.Fatalf("expected empty %s", w.Body.String())
	}
}

func TestHTMLBodySanitized(t *testing.T) {
	s, db := testServer(t)
	if err := db.UpdateBody(context.Background(), "m1", `<p>Hi<script>alert(1)</script></p>`); err != nil {
		t.Fatal(err)
	}
	w := req(t, s.Handler(), http.MethodGet, "/api/messages/m1?body=1")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var got struct {
		Message map[string]any `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Message["is_html"] != true {
		t.Fatalf("is_html %#v", got.Message["is_html"])
	}
	body, _ := got.Message["body"].(string)
	if !strings.Contains(body, "Hi") || strings.Contains(strings.ToLower(body), "script") {
		t.Fatalf("body %q", body)
	}
	stored, err := db.GetMessage(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.Body, "<script>") {
		t.Fatalf("stored body changed: %q", stored.Body)
	}
}

func TestDetailAndBody(t *testing.T) {
	s, db := testServer(t)
	h := s.Handler()
	w := req(t, h, http.MethodGet, "/api/messages/m1")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	w = req(t, h, http.MethodPost, "/api/messages/m1/body")
	if w.Code != 200 {
		t.Fatalf("body %d %s", w.Code, w.Body.String())
	}
	got, err := db.GetMessage(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "fetched body" {
		t.Fatalf("body %q", got.Body)
	}
}

func TestStats(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	w := req(t, h, http.MethodGet, "/api/stats")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	w = req(t, h, http.MethodGet, "/api/stats/top_senders")
	if w.Code != 200 {
		t.Fatalf("stat %d %s", w.Code, w.Body.String())
	}
	var table struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Columns) != 2 || table.Columns[0] != "from_email" {
		t.Fatalf("columns %#v", table.Columns)
	}
}

func TestBodyRequiresExisting(t *testing.T) {
	s, _ := testServer(t)
	called := false
	s.FetchBody = func(ctx context.Context, id string) (string, error) {
		called = true
		return "x", nil
	}
	h := s.Handler()
	w := req(t, h, http.MethodPost, "/api/messages/missing/body")
	if w.Code != http.StatusNotFound {
		t.Fatalf("code %d %s", w.Code, w.Body.String())
	}
	if called {
		t.Fatal("gmail fetch should not run")
	}
}

func TestAPIRequiresToken(t *testing.T) {
	s, _ := testServer(t)
	s.Token = "secret"
	h := s.Handler()
	w := req(t, h, http.MethodGet, "/api/messages")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code %d", w.Code)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/messages?t=secret", nil)
	r.RemoteAddr = "127.0.0.1:1"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("query token must not work %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/messages", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("X-Token", "secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("header token %d %s", w.Code, w.Body.String())
	}
}

func TestEmptyTokenDeniesAPI(t *testing.T) {
	s, _ := testServer(t)
	s.Token = ""
	w := req(t, s.Handler(), http.MethodGet, "/api/messages")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code %d", w.Code)
	}
}

func TestSessionCookie(t *testing.T) {
	s, _ := testServer(t)
	w := req(t, s.Handler(), http.MethodGet, "/")
	if !strings.Contains(w.Header().Get("Set-Cookie"), "session=test") {
		t.Fatalf("cookie %q", w.Header().Get("Set-Cookie"))
	}
}

func TestDuckUIDisabled(t *testing.T) {
	s, _ := testServer(t)
	w := req(t, s.Handler(), http.MethodPost, "/api/duckdb-ui")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code %d %s", w.Code, w.Body.String())
	}
}

func TestStatusSchemaSQL(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	w := req(t, h, http.MethodGet, "/api/status")
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	var env query.Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.SchemaVersion != 1 || env.Phase != "idle" {
		t.Fatalf("%+v", env)
	}
	w = req(t, h, http.MethodGet, "/api/schema")
	if w.Code != 200 {
		t.Fatalf("schema %d %s", w.Code, w.Body.String())
	}
	body := strings.NewReader(`{"query":"SELECT 1 AS n"}`)
	r := httptest.NewRequest(http.MethodPost, "/api/sql", body)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("X-Token", "test")
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, r)
	if rw.Code != 200 {
		t.Fatalf("sql %d %s", rw.Code, rw.Body.String())
	}
}

func TestSQLExploitDoesNotDelete(t *testing.T) {
	s, db := testServer(t)
	body := strings.NewReader(`{"query":"SELECT 1 AS \"--\"; DELETE FROM messages"}`)
	r := httptest.NewRequest(http.MethodPost, "/api/sql", body)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("X-Token", "test")
	rw := httptest.NewRecorder()
	s.Handler().ServeHTTP(rw, r)
	if rw.Code == 200 {
		t.Fatalf("expected exploit reject %d %s", rw.Code, rw.Body.String())
	}
	got, err := db.GetMessage(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "m1" {
		t.Fatalf("data lost: %+v", got)
	}
}

func TestSyncNow(t *testing.T) {
	s, _ := testServer(t)
	called := false
	s.Sync = func(ctx context.Context, opt mailsync.Options) error {
		called = true
		return nil
	}
	body := strings.NewReader(`{"wait":true}`)
	r := httptest.NewRequest(http.MethodPost, "/api/sync", body)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("X-Token", "test")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !called {
		t.Fatalf("sync %d %s called=%v", w.Code, w.Body.String(), called)
	}
}

func TestRejectNonLoopback(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "8.8.8.8:9"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("code %d", w.Code)
	}
}
