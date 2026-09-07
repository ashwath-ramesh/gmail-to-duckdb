package web

import (
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/stats"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

type Server struct {
	DB        *store.DB
	Token     string
	FetchBody func(ctx context.Context, id string) (string, error)
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		return true
	}
	if r.Header.Get("X-Token") == s.Token {
		return true
	}
	return r.URL.Query().Get("t") == s.Token
}

func ListenAndServe(ctx context.Context, port string, h http.Handler) error {
	addr := net.JoinHostPort("127.0.0.1", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	err = srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
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
	if v := q.Get("after_date"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.AfterDate = t
			f.AfterID = q.Get("after_id")
		}
	}
	msgs, err := s.DB.ListMessages(r.Context(), f)
	if err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"messages": msgsJSON(msgs)})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	msg, err := s.DB.GetMessage(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err, http.StatusNotFound)
		return
	}
	writeJSON(w, msgJSON(msg))
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
	_, err := s.DB.SQL().ExecContext(r.Context(), "CALL start_ui()")
	if err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"url": "http://127.0.0.1:4213"})
}

func msgsJSON(msgs []store.Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, msgJSON(m))
	}
	return out
}

func msgJSON(m store.Message) map[string]any {
	return map[string]any{
		"id":            m.ID,
		"thread_id":     m.ThreadID,
		"internal_date": m.InternalDate.UTC().Format(time.RFC3339),
		"from_name":     m.FromName,
		"from_email":    m.FromEmail,
		"to_emails":     m.ToEmails,
		"subject":       m.Subject,
		"snippet":       m.Snippet,
		"body":          m.Body,
		"label_ids":     m.LabelIDs,
		"is_read":       m.IsRead,
		"is_outgoing":   m.IsOutgoing,
		"has_body":      m.HasBody,
	}
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
