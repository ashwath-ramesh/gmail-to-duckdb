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

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
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
	s := &Server{DB: db, FetchBody: func(ctx context.Context, id string) (string, error) {
		return "fetched body", nil
	}}
	return s, db
}

func req(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = "127.0.0.1:1234"
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
	w := req(t, s.Handler(), http.MethodGet, "/api/messages/m1")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["is_html"] != true {
		t.Fatalf("is_html %#v", got["is_html"])
	}
	body, _ := got["body"].(string)
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
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
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
	w = req(t, h, http.MethodGet, "/api/messages?t=secret")
	if w.Code != 200 {
		t.Fatalf("token query %d %s", w.Code, w.Body.String())
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
