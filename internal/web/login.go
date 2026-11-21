package web

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
)

// loginCodeTTL is how long a one-time UI login code stays valid.
const loginCodeTTL = 2 * time.Minute

var errBadLoginCode = errString("login link expired or already used; run gmail-to-duckdb ui for a new link")

type loginCodes struct {
	mu    sync.Mutex
	codes map[string]time.Time
}

// NewLoginCode returns a random code that /api/login exchanges once for the API token.
func (s *Server) NewLoginCode() (string, error) {
	code, err := NewToken()
	if err != nil {
		return "", err
	}
	now := time.Now()
	s.login.mu.Lock()
	defer s.login.mu.Unlock()
	if s.login.codes == nil {
		s.login.codes = map[string]time.Time{}
	}
	for c, exp := range s.login.codes {
		if now.After(exp) {
			delete(s.login.codes, c)
		}
	}
	s.login.codes[code] = now.Add(loginCodeTTL)
	return code, nil
}

func (s *Server) useLoginCode(code string) bool {
	s.login.mu.Lock()
	defer s.login.mu.Unlock()
	exp, ok := s.login.codes[code]
	delete(s.login.codes, code)
	return ok && time.Now().Before(exp)
}

// loginExchange trades a one-time code for the API token. It is the only
// /api route that does not need X-Token.
func (s *Server) loginExchange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.Code == "" || s.Token == "" {
		writeErr(w, errBadLoginCode, http.StatusUnauthorized)
		return
	}
	if !s.useLoginCode(req.Code) {
		writeErr(w, errBadLoginCode, http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]string{"token": s.Token})
}

// loginCode gives an authenticated caller (the CLI) a new one-time code.
func (s *Server) loginCode(w http.ResponseWriter, r *http.Request) {
	code, err := s.NewLoginCode()
	if err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"code": code})
}

// LoginLink is the UI address that signs the browser in with code.
func LoginLink(baseURL, code string) string {
	return strings.TrimSuffix(baseURL, "/") + "/#code=" + code
}

// LauncherPath is the private HTML file that redirects the browser to a login link.
func LauncherPath(dbPath string) string {
	ext := filepath.Ext(dbPath)
	return strings.TrimSuffix(dbPath, ext) + ".open.html"
}

// WriteLauncher writes an owner-only HTML file that redirects to link and
// returns its path. Open the file, not the link, so that the one-time code
// never appears in process arguments that other users can read.
func WriteLauncher(dbPath, link string) (string, error) {
	esc := html.EscapeString(link)
	page := `<!doctype html>
<meta charset="utf-8">
<meta name="referrer" content="no-referrer">
<meta http-equiv="refresh" content="0;url=` + esc + `">
<title>Gmail to DuckDB</title>
<p><a href="` + esc + `">Open Gmail to DuckDB</a></p>
`
	path, err := filepath.Abs(LauncherPath(dbPath))
	if err != nil {
		return "", err
	}
	if err := privfile.Write(path, []byte(page)); err != nil {
		return "", err
	}
	return path, nil
}

func RemoveLauncher(dbPath string) error {
	err := os.Remove(LauncherPath(dbPath))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
