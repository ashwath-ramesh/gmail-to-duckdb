package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	mailsync "github.com/ashwath-ramesh/gmail-to-duckdb/internal/sync"
)

func testServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	dir := t.TempDir()
	if goruntime.GOOS == "windows" {
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
	ctx := context.Background()
	if err := db.UpsertMessages(ctx, []store.Message{{
		ID: "m1", ThreadID: "t1", InternalDate: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
		FromEmail: "a@x.com", Subject: "Hello", Snippet: "sn", LabelIDs: []string{"INBOX"},
	}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{DB: db, Token: "test", AllowedHosts: AllowedHosts(8080), FetchBody: func(ctx context.Context, id string) (string, error) {
		return "fetched body", nil
	}}
	return s, db
}

func req(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	return doReq(t, h, method, path, "127.0.0.1:8080", nil, nil)
}

func doReq(t *testing.T, h http.Handler, method, path, host string, hdr http.Header, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, body)
	r.Host = host
	r.RemoteAddr = "127.0.0.1:1234"
	for k, vs := range hdr {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	if r.Header.Get("X-Token") == "" && r.Header.Get("Cookie") == "" {
		r.Header.Set("X-Token", "test")
	}
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
	r.Host = "127.0.0.1:8080"
	r.RemoteAddr = "127.0.0.1:1"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("query token must not work %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/messages", nil)
	r.Host = "127.0.0.1:8080"
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
	createUITableMacro(t, s, "start_ui", "'started'")
	createUITableMacro(t, s, "start_ui_server", "'started'")
	createUITableMacro(t, s, "get_ui_url", "'http://[::1]:55555'")
	w := req(t, s.Handler(), http.MethodPost, "/api/duckdb-ui")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"url"`) {
		t.Fatalf("opt-out returned url %s", w.Body.String())
	}
}

func TestDuckUIReturnsActualURL(t *testing.T) {
	s, _ := testServer(t)
	s.AllowDuckUI = true
	createUITableMacro(t, s, "start_ui", "'started'")
	createUITableMacro(t, s, "start_ui_server", "'started'")
	createUITableMacro(t, s, "get_ui_url", "'http://[::1]:55555'")
	w := req(t, s.Handler(), http.MethodPost, "/api/duckdb-ui")
	if w.Code != 200 {
		t.Fatalf("code %d %s", w.Code, w.Body.String())
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.URL != "http://[::1]:55555" {
		t.Fatalf("url %q", out.URL)
	}
}

func TestDuckUIURLErrorIsNotSuccess(t *testing.T) {
	s, _ := testServer(t)
	s.AllowDuckUI = true
	createUITableMacro(t, s, "start_ui", "'started'")
	createUITableMacro(t, s, "start_ui_server", "'started'")
	createUITableMacro(t, s, "get_ui_url", "error('no url')")
	w := req(t, s.Handler(), http.MethodPost, "/api/duckdb-ui")
	if w.Code == 200 {
		t.Fatalf("success %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"url"`) {
		t.Fatalf("error returned url %s", w.Body.String())
	}
}

func createUITableMacro(t *testing.T, s *Server, name, expr string) {
	t.Helper()
	q := "CREATE OR REPLACE MACRO " + name + "() AS TABLE SELECT " + expr
	if _, err := s.DB.SQL().Exec(q); err != nil {
		t.Fatal(err)
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
	if env.SchemaVersion != 2 || env.Phase != "idle" {
		t.Fatalf("%+v", env)
	}
	if env.UntrustedContent {
		t.Fatal("status must stay trusted")
	}
	w = req(t, h, http.MethodGet, "/api/schema")
	if w.Code != 200 {
		t.Fatalf("schema %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.UntrustedContent {
		t.Fatal("schema must stay trusted")
	}
	for _, host := range []string{"127.0.0.1:8080", "localhost:8080"} {
		body := strings.NewReader(`{"query":"SELECT 1 AS n"}`)
		rw := doReq(t, h, http.MethodPost, "/api/sql", host, nil, body)
		if rw.Code != 200 {
			t.Fatalf("sql %s %d %s", host, rw.Code, rw.Body.String())
		}
		if err := json.Unmarshal(rw.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		if env.SQL == nil || env.ResultCount != 1 || !env.UntrustedContent {
			t.Fatalf("%s %+v", host, env)
		}
		if len(env.UntrustedFields) != 1 || env.UntrustedFields[0] != "n" {
			t.Fatalf("%s fields %#v", host, env.UntrustedFields)
		}
		if fmt.Sprint(env.SQL.Rows[0][0]) != "1" {
			t.Fatalf("%s payload %#v", host, env.SQL.Rows)
		}
	}
	body := strings.NewReader(`{"query":"SELECT row_to_json(messages) AS data FROM messages"}`)
	rw := doReq(t, h, http.MethodPost, "/api/sql", "127.0.0.1:8080", nil, body)
	if rw.Code != 200 {
		t.Fatalf("rowjson %d %s", rw.Code, rw.Body.String())
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.SQL == nil || !env.UntrustedContent || len(env.UntrustedFields) != 1 || env.UntrustedFields[0] != "data" {
		t.Fatalf("rowjson %+v", env)
	}
	raw, err := json.Marshal(env.SQL.Rows)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Hello") {
		t.Fatalf("rowjson payload %s", raw)
	}
}

func TestSQLExploitDoesNotDelete(t *testing.T) {
	s, db := testServer(t)
	body := strings.NewReader(`{"query":"SELECT 1 AS \"--\"; DELETE FROM messages"}`)
	r := httptest.NewRequest(http.MethodPost, "/api/sql", body)
	r.Host = "127.0.0.1:8080"
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
	r.Host = "127.0.0.1:8080"
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
	r.Host = "127.0.0.1:8080"
	r.RemoteAddr = "8.8.8.8:9"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("code %d", w.Code)
	}
}

func TestAttackerHostDoesNotSetCookie(t *testing.T) {
	s, _ := testServer(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:9"
	r.Host = "evil.example"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("code %d", w.Code)
	}
	if strings.Contains(w.Header().Get("Set-Cookie"), "session=") {
		t.Fatalf("cookie issued: %q", w.Header().Get("Set-Cookie"))
	}
}

func TestAttackerHostDoesNotRunSQL(t *testing.T) {
	s, db := testServer(t)
	body := strings.NewReader(`{"query":"DELETE FROM messages","write":true}`)
	r := httptest.NewRequest(http.MethodPost, "/api/sql", body)
	r.RemoteAddr = "127.0.0.1:9"
	r.Host = "evil.example"
	r.Header.Set("X-Token", "test")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("code %d %s", w.Code, w.Body.String())
	}
	got, err := db.GetMessage(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "m1" {
		t.Fatalf("sql effect: %+v", got)
	}
}

func TestAbsentHostsDenied(t *testing.T) {
	s, _ := testServer(t)
	s.AllowedHosts = nil
	w := req(t, s.Handler(), http.MethodGet, "/")
	if w.Code != http.StatusForbidden {
		t.Fatalf("code %d", w.Code)
	}
	if strings.Contains(w.Header().Get("Set-Cookie"), "session=") {
		t.Fatalf("cookie issued: %q", w.Header().Get("Set-Cookie"))
	}
}

func TestLocalhostAndCustomPort(t *testing.T) {
	s, _ := testServer(t)
	s.AllowedHosts = AllowedHosts(4321)
	h := s.Handler()
	for _, host := range []string{"127.0.0.1:4321", "localhost:4321"} {
		w := doReq(t, h, http.MethodGet, "/api/status", host, nil, nil)
		if w.Code != 200 {
			t.Fatalf("%s %d %s", host, w.Code, w.Body.String())
		}
	}
	w := doReq(t, h, http.MethodGet, "/api/status", "127.0.0.1:8080", nil, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("wrong port %d", w.Code)
	}
	s.AllowedHosts = AllowedHosts(80)
	h = s.Handler()
	for _, host := range []string{"127.0.0.1", "127.0.0.1:80", "localhost"} {
		w := doReq(t, h, http.MethodGet, "/api/status", host, nil, nil)
		if w.Code != 200 {
			t.Fatalf("port80 %s %d %s", host, w.Code, w.Body.String())
		}
	}
}

func TestOriginAndFetchSite(t *testing.T) {
	s, db := testServer(t)
	h := s.Handler()
	host := "127.0.0.1:8080"
	w := doReq(t, h, http.MethodGet, "/api/status", host, http.Header{"X-Token": {"test"}}, nil)
	if w.Code != 200 {
		t.Fatalf("cli %d %s", w.Code, w.Body.String())
	}
	w = doReq(t, h, http.MethodGet, "/api/messages", host, http.Header{
		"Cookie":         {"session=test"},
		"Origin":         {"http://127.0.0.1:8080"},
		"Sec-Fetch-Site": {"same-origin"},
	}, nil)
	if w.Code != 200 {
		t.Fatalf("cookie %d %s", w.Code, w.Body.String())
	}
	w = doReq(t, h, http.MethodGet, "/", host, http.Header{"Sec-Fetch-Site": {"none"}}, nil)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Set-Cookie"), "session=test") {
		t.Fatalf("nav %d cookie %q", w.Code, w.Header().Get("Set-Cookie"))
	}

	deny := []http.Header{
		{"Origin": {"http://evil.example"}},
		{"Origin": {"http://localhost:9999"}},
		{"Origin": {"null"}},
		{"Origin": {"http://127.0.0.1:8080/"}},
		{"Origin": {"http://user@127.0.0.1:8080"}},
		{"Origin": {"http://127.0.0.1:8080?q=1"}},
		{"Origin": {"http://127.0.0.1:8080#f"}},
		{"Origin": {"https://127.0.0.1:8080"}},
		{"Origin": {"http://127.0.0.1:8080", "http://127.0.0.1:8080"}},
		{"Sec-Fetch-Site": {"cross-site"}},
		{"Sec-Fetch-Site": {"same-site"}},
		{"X-Forwarded-Host": {"127.0.0.1:8080"}},
	}
	for i, hdr := range deny {
		if hdr.Get("X-Forwarded-Host") != "" {
			w = doReq(t, h, http.MethodGet, "/", "evil.example", hdr, nil)
		} else {
			w = doReq(t, h, http.MethodGet, "/", host, hdr, nil)
		}
		if w.Code != http.StatusForbidden {
			t.Fatalf("deny %d code %d hdr %v", i, w.Code, hdr)
		}
		if strings.Contains(w.Header().Get("Set-Cookie"), "session=") {
			t.Fatalf("deny %d cookie %q", i, w.Header().Get("Set-Cookie"))
		}
	}
	w = doReq(t, h, http.MethodGet, "/", host, http.Header{"X-Forwarded-Host": {"evil.example"}}, nil)
	if w.Code != 200 {
		t.Fatalf("ignore forwarded %d", w.Code)
	}
	body := strings.NewReader(`{"query":"DELETE FROM messages","write":true}`)
	w = doReq(t, h, http.MethodPost, "/api/sql", host, http.Header{"Origin": {"http://evil.example"}, "X-Token": {"test"}}, body)
	if w.Code != http.StatusForbidden {
		t.Fatalf("origin sql %d %s", w.Code, w.Body.String())
	}
	got, err := db.GetMessage(context.Background(), "m1")
	if err != nil || got.ID != "m1" {
		t.Fatalf("sql effect: %v %+v", err, got)
	}
}

func TestBindListenerUsesActualPort(t *testing.T) {
	s, _ := testServer(t)
	ln, err := Listen("0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	u, h, err := s.BindListener(ln)
	if err != nil {
		t.Fatal(err)
	}
	port := strings.TrimPrefix(u, "http://127.0.0.1:")
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatalf("url %s", u)
	}
	w := doReq(t, h, http.MethodGet, "/api/status", "127.0.0.1:"+port, nil, nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	w = doReq(t, h, http.MethodGet, "/api/status", "localhost:"+port, nil, nil)
	if w.Code != 200 {
		t.Fatalf("localhost %d %s", w.Code, w.Body.String())
	}
}
