package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
)

// noTokenReq sends a request with no X-Token, as a browser does before login.
func noTokenReq(t *testing.T, h http.Handler, method, path, body string, hdr http.Header) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "127.0.0.1:8080"
	r.RemoteAddr = "127.0.0.1:1234"
	for k, vs := range hdr {
		r.Header[k] = vs
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func exchange(t *testing.T, h http.Handler, code string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"code": code})
	return noTokenReq(t, h, http.MethodPost, "/api/login", string(b), http.Header{
		"Content-Type":   {"application/json"},
		"Origin":         {"http://127.0.0.1:8080"},
		"Sec-Fetch-Site": {"same-origin"},
	})
}

func TestLoginCodeWorksOnce(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	code, err := s.NewLoginCode()
	if err != nil {
		t.Fatal(err)
	}
	w := exchange(t, h, code)
	if w.Code != 200 {
		t.Fatalf("first use %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Token != "test" {
		t.Fatalf("token %q %v", out.Token, err)
	}
	if w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("login must not set a cookie: %q", w.Header().Get("Set-Cookie"))
	}
	if w := exchange(t, h, code); w.Code != http.StatusUnauthorized {
		t.Fatalf("second use %d", w.Code)
	}
	if w := exchange(t, h, "not-a-code"); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad code %d", w.Code)
	}
}

func TestLoginCodeExpires(t *testing.T) {
	s, _ := testServer(t)
	code, err := s.NewLoginCode()
	if err != nil {
		t.Fatal(err)
	}
	s.login.mu.Lock()
	s.login.codes[code] = time.Now().Add(-time.Second)
	s.login.mu.Unlock()
	if w := exchange(t, s.Handler(), code); w.Code != http.StatusUnauthorized {
		t.Fatalf("expired %d", w.Code)
	}
}

func TestLoginRejectsCrossSite(t *testing.T) {
	s, _ := testServer(t)
	code, err := s.NewLoginCode()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"code": code})
	w := noTokenReq(t, s.Handler(), http.MethodPost, "/api/login", string(b), http.Header{
		"Origin":         {"http://evil.example"},
		"Sec-Fetch-Site": {"cross-site"},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site login %d", w.Code)
	}
}

func TestLoginCodeEndpointNeedsToken(t *testing.T) {
	s, _ := testServer(t)
	h := s.Handler()
	if w := noTokenReq(t, h, http.MethodPost, "/api/login-code", "{}", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token %d", w.Code)
	}
	w := req(t, h, http.MethodPost, "/api/login-code")
	if w.Code != 200 {
		t.Fatalf("with token %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Code == "" {
		t.Fatalf("code %q %v", out.Code, err)
	}
	if w := exchange(t, h, out.Code); w.Code != 200 {
		t.Fatalf("minted code %d", w.Code)
	}
}

func TestAPIWithoutTokenIsUnauthorized(t *testing.T) {
	s, _ := testServer(t)
	w := noTokenReq(t, s.Handler(), http.MethodGet, "/api/status", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code %d", w.Code)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control %q", w.Header().Get("Cache-Control"))
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e.Error == "" {
		t.Fatalf("want JSON error, got %q", w.Body.String())
	}
}

func TestWriteLauncherIsPrivateAndEscaped(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "mail.duckdb")
	link := LoginLink("http://127.0.0.1:8080", `abc"<x>`)
	path, err := WriteLauncher(db, link)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) || path != filepath.Join(dir, "mail.open.html") {
		t.Fatalf("path %s", path)
	}
	if err := privfile.Check(path); err != nil {
		t.Fatalf("launcher not private: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	if strings.Contains(page, `abc"<x>`) || !strings.Contains(page, "http://127.0.0.1:8080/#code=abc&#34;&lt;x&gt;") {
		t.Fatalf("launcher not escaped: %s", page)
	}
	if err := RemoveLauncher(db); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("launcher still present: %v", err)
	}
}

func TestHTMLBodyRendersInSandbox(t *testing.T) {
	b, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	if !strings.Contains(js, `setAttribute("sandbox", "allow-popups allow-popups-to-escape-sandbox")`) || !strings.Contains(js, "srcdoc") {
		t.Fatal("HTML body must render in a sandboxed srcdoc iframe")
	}
	for _, bad := range []string{"body.innerHTML", "allow-scripts", "allow-same-origin", "allow-forms", "document.cookie"} {
		if strings.Contains(js, bad) {
			t.Fatalf("app.js must not contain %q", bad)
		}
	}
}

func TestSyncOffShowsReason(t *testing.T) {
	s, _ := testServer(t)
	s.NoSyncReason = "Gmail sign-in failed: oauth: access_denied"
	h := s.Handler()
	w := req(t, h, http.MethodPost, "/api/sync")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "access_denied") {
		t.Fatalf("sync %d %s", w.Code, w.Body.String())
	}
	w = req(t, h, http.MethodGet, "/api/status")
	if !strings.Contains(w.Body.String(), "access_denied") {
		t.Fatalf("status must show why sync is off: %s", w.Body.String())
	}
}
