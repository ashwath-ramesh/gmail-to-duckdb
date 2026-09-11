package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/search"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/stats"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	mailsync "github.com/ashwath-ramesh/gmail-to-duckdb/internal/sync"
)

type Server struct {
	DB           *store.DB
	Token        string
	FetchBody    func(ctx context.Context, id string) error
	Sync         func(ctx context.Context, opt mailsync.Options) error
	SyncCtx      context.Context
	AllowDuckUI  bool
	AllowedHosts []string
	rt           runtime
}

func AllowedHosts(port int) []string {
	if port <= 0 {
		return nil
	}
	if port == 80 {
		return []string{"127.0.0.1", "localhost", "127.0.0.1:80", "localhost:80"}
	}
	p := strconv.Itoa(port)
	return []string{"127.0.0.1:" + p, "localhost:" + p}
}

func (s *Server) BindListener(ln net.Listener) (string, http.Handler, error) {
	ta, ok := ln.Addr().(*net.TCPAddr)
	if !ok || ta.Port <= 0 {
		return "", nil, fmt.Errorf("listen port unavailable")
	}
	s.AllowedHosts = AllowedHosts(ta.Port)
	return Addr(strconv.Itoa(ta.Port)), s.Handler(), nil
}

func (s *Server) Handler() http.Handler {
	static, _ := fs.Sub(staticFS, "static")
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /api/messages", s.list)
	mux.HandleFunc("GET /api/messages/{id}", s.get)
	mux.HandleFunc("POST /api/messages/{id}/body", s.body)
	mux.HandleFunc("GET /api/stats", s.listStats)
	mux.HandleFunc("GET /api/stats/{name}", s.stat)
	mux.HandleFunc("POST /api/duckdb-ui", s.duckUI)
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/schema", s.schema)
	mux.HandleFunc("POST /api/sql", s.sql)
	mux.HandleFunc("POST /api/sync", s.syncNow)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-src 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !loopbackOnly(r) || !s.allowHost(r) || !s.allowOrigin(r) || !s.allowFetchSite(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && !s.validToken(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) validToken(r *http.Request) bool {
	if s.Token == "" {
		return false
	}
	if tokenEq(r.Header.Get("X-Token"), s.Token) {
		return true
	}
	c, err := r.Cookie("session")
	return err == nil && tokenEq(c.Value, s.Token)
}

func tokenEq(got, want string) bool {
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func Listen(port string) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
}

func Serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	err := srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func ListenAndServe(ctx context.Context, port string, h http.Handler) error {
	ln, err := Listen(port)
	if err != nil {
		return err
	}
	return Serve(ctx, ln, h)
}

func Addr(port string) string {
	return "http://127.0.0.1:" + port
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	b, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.Token != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    s.Token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ListFilter{
		Unread: q.Get("unread") == "1",
		Label:  q.Get("label"),
		From:   q.Get("from"),
		Query:  q.Get("q"),
		Limit:  50,
	}
	if q.Get("q") == "" {
		if v := q.Get("after_date"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				f.AfterDate = t
				f.AfterID = q.Get("after_id")
			}
		}
	} else if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 5000 {
				n = 5000
			}
			f.Offset = n
		}
	}
	msgs, err := s.DB.ListMessages(r.Context(), f)
	if err != nil {
		code := http.StatusInternalServerError
		if search.IsValidation(err) {
			code = http.StatusBadRequest
		}
		writeErr(w, err, code)
		return
	}
	env, err := s.liveStatus(r.Context())
	if err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	out := make([]query.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, query.MessageFromStore(m, false))
	}
	env.Messages = out
	if env.Messages == nil {
		env.Messages = []query.Message{}
	}
	env.ResultCount = len(out)
	env.Truncated = len(out) == f.Limit
	query.MarkMailUntrusted(&env)
	writeJSON(w, env)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	includeBody := r.URL.Query().Get("body") == "1"
	env, err := query.Get(r.Context(), s.DB, r.PathValue("id"), includeBody)
	if err != nil {
		writeErr(w, err, http.StatusNotFound)
		return
	}
	writeJSON(w, env)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	env, err := s.liveStatus(r.Context())
	if err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, env)
}

func (s *Server) schema(w http.ResponseWriter, r *http.Request) {
	env, err := query.SchemaInfo(r.Context(), s.DB)
	if err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, env)
}

func (s *Server) sql(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
		Write bool   `json:"write"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, err, http.StatusBadRequest)
		return
	}
	env, err := query.SQL(r.Context(), s.DB, req.Query, req.Write)
	if err != nil {
		writeErr(w, err, http.StatusBadRequest)
		return
	}
	if req.Write {
		s.noteChange()
	}
	writeJSON(w, env)
}

func (s *Server) syncNow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Full   bool `json:"full"`
		Bodies bool `json:"bodies"`
		Wait   bool `json:"wait"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, err, http.StatusBadRequest)
			return
		}
	}
	opt := mailsync.Options{Full: req.Full, Bodies: req.Bodies}
	if req.Wait {
		if err := s.WaitSync(r.Context(), opt); err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, errSyncBusy) {
				code = http.StatusConflict
			} else if !errors.Is(err, errNoSync) {
				code = http.StatusBadGateway
			}
			writeErr(w, err, code)
			return
		}
	} else {
		if err := s.StartSync(s.syncContext(), opt); err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, errSyncBusy) {
				code = http.StatusConflict
			}
			writeErr(w, err, code)
			return
		}
	}
	env, err := s.liveStatus(r.Context())
	if err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, env)
}

func (s *Server) body(w http.ResponseWriter, r *http.Request) {
	if s.FetchBody == nil {
		writeErr(w, errNoFetch, http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	if _, err := s.DB.GetMessage(r.Context(), id); err != nil {
		writeErr(w, err, http.StatusNotFound)
		return
	}
	if err := s.FetchBody(r.Context(), id); err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, mailsync.ErrNotFound) {
			code = http.StatusNotFound
		}
		writeErr(w, err, code)
		return
	}
	s.noteChange()
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) listStats(w http.ResponseWriter, r *http.Request) {
	qs, err := stats.All()
	if err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	out := make([]map[string]string, 0, len(qs))
	for _, q := range qs {
		out = append(out, map[string]string{"id": q.ID, "name": q.Name})
	}
	writeJSON(w, map[string]any{"stats": out})
}

func (s *Server) stat(w http.ResponseWriter, r *http.Request) {
	q, err := stats.Get(r.PathValue("name"))
	if err != nil {
		writeErr(w, err, http.StatusNotFound)
		return
	}
	res, err := s.DB.ExecSQL(r.Context(), q.SQL)
	if err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, res)
}

func (s *Server) duckUI(w http.ResponseWriter, r *http.Request) {
	if !s.AllowDuckUI {
		writeErr(w, errNoDuckUI, http.StatusBadRequest)
		return
	}
	if _, err := s.DB.SQL().ExecContext(r.Context(), "CALL start_ui_server()"); err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	_ = s.DB.DisableSearchAccel(r.Context())
	s.noteChange()
	var u string
	if err := s.DB.SQL().QueryRowContext(r.Context(), "CALL get_ui_url()").Scan(&u); err != nil || u == "" {
		if err == nil {
			err = errNoUIURL
		}
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"url": u})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

var errNoFetch = errString("credentials not loaded; run sync or pass --credentials")
var errNoDuckUI = errString("DuckDB UI is disabled; pass --duckdb-ui to serve")
var errNoUIURL = errString("DuckDB UI URL unavailable")

type errString string

func (e errString) Error() string { return string(e) }

func loopbackOnly(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) allowHost(r *http.Request) bool {
	if len(s.AllowedHosts) == 0 {
		return false
	}
	got := canonicalHost(r.Host)
	if got == "" {
		return false
	}
	for _, a := range s.AllowedHosts {
		if canonicalHost(a) == got {
			return true
		}
	}
	return false
}

func canonicalHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return ""
	}
	name, port, err := net.SplitHostPort(h)
	if err != nil {
		if strings.Contains(h, ":") {
			return ""
		}
		return h
	}
	if name == "" || port == "" {
		return ""
	}
	if port == "80" {
		return name
	}
	return name + ":" + port
}

func (s *Server) allowOrigin(r *http.Request) bool {
	vals := r.Header.Values("Origin")
	if len(vals) == 0 {
		return true
	}
	if len(vals) != 1 {
		return false
	}
	got, ok := parseHTTPOrigin(vals[0])
	return ok && got == canonicalHost(r.Host)
}

func parseHTTPOrigin(raw string) (string, bool) {
	if raw == "" || strings.EqualFold(raw, "null") {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if u.Scheme != "http" || u.User != nil || u.Opaque != "" || u.Host == "" {
		return "", false
	}
	if u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	h := canonicalHost(u.Host)
	if h == "" {
		return "", false
	}
	return h, true
}

func (s *Server) allowFetchSite(r *http.Request) bool {
	vals := r.Header.Values("Sec-Fetch-Site")
	if len(vals) == 0 {
		return true
	}
	if len(vals) != 1 {
		return false
	}
	switch strings.ToLower(vals[0]) {
	case "none", "same-origin":
		return true
	default:
		return false
	}
}
