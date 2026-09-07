package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/stats"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	mailsync "github.com/ashwath-ramesh/gmail-to-duckdb/internal/sync"
)

type Server struct {
	DB          *store.DB
	Token       string
	FetchBody   func(ctx context.Context, id string) (string, error)
	Sync        func(ctx context.Context, opt mailsync.Options) error
	SyncCtx     context.Context
	AllowDuckUI bool
	rt          runtime
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
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/schema", s.schema)
	mux.HandleFunc("POST /api/sql", s.sql)
	mux.HandleFunc("POST /api/sync", s.syncNow)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-src 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !loopbackOnly(r) {
			http.Error(w, "loopback only", http.StatusForbidden)
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
	if r.Header.Get("X-Token") == s.Token {
		return true
	}
	c, err := r.Cookie("session")
	return err == nil && c.Value == s.Token
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
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	env, err := query.Status(r.Context(), s.DB)
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
	body, err := s.FetchBody(r.Context(), id)
	if err != nil {
		writeErr(w, err, http.StatusBadGateway)
		return
	}
	if err := s.DB.UpdateBody(r.Context(), id, body); err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
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
	_, err := s.DB.SQL().ExecContext(r.Context(), "CALL start_ui()")
	if err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"url": "http://127.0.0.1:4213"})
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
