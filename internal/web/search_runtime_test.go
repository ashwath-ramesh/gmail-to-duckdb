package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

func seedStatus(t *testing.T, s *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.SeedStatus(ctx)
}

func startStatus(t *testing.T, s *Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s.StartStatus(ctx)
	t.Cleanup(func() {
		cancel()
		s.StopStatus()
	})
}

func holdDuckPool(t *testing.T, db *store.DB) func() {
	t.Helper()
	ctx := context.Background()
	var conns []*sql.Conn
	for i := 0; i < 2; i++ {
		c, err := db.SQL().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.ExecContext(ctx, "BEGIN TRANSACTION READ ONLY"); err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	return func() {
		for _, c := range conns {
			_, _ = c.ExecContext(context.Background(), "ROLLBACK")
			_ = c.Close()
		}
	}
}

func TestHealthAndStatusWhilePoolHeld(t *testing.T) {
	s, db := testServer(t)
	h := s.Handler()
	release := holdDuckPool(t, db)
	defer release()
	start := time.Now()
	w := req(t, h, http.MethodGet, "/api/health")
	if w.Code != 200 {
		t.Fatalf("health %d %s", w.Code, w.Body.String())
	}
	st := req(t, h, http.MethodGet, "/api/status")
	if st.Code != 200 {
		t.Fatalf("status %d %s", st.Code, st.Body.String())
	}
	if time.Since(start) > time.Second {
		t.Fatalf("cold status blocked %s", time.Since(start))
	}
	var env query.Envelope
	if err := json.Unmarshal(st.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Phase != "initializing" {
		t.Fatalf("cold phase %q", env.Phase)
	}
}

func TestExpiredStatusWhilePoolHeld(t *testing.T) {
	s, db := testServer(t)
	seedStatus(t, s)
	s.noteChange()
	h := s.Handler()
	release := holdDuckPool(t, db)
	defer release()
	start := time.Now()
	st := req(t, h, http.MethodGet, "/api/status")
	if st.Code != 200 {
		t.Fatalf("status %d %s", st.Code, st.Body.String())
	}
	if time.Since(start) > time.Second {
		t.Fatalf("cached status blocked %s", time.Since(start))
	}
	var env query.Envelope
	if err := json.Unmarshal(st.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.BodyCoverage.Total != 1 {
		t.Fatalf("stale cache lost %+v", env.BodyCoverage)
	}
}

func TestStatusKeepsSyncErrorAndNoteChange(t *testing.T) {
	s, _ := testServer(t)
	seedStatus(t, s)
	s.endSync(errors.New("sync boom"))
	env, err := s.liveStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if env.LastError != "sync boom" {
		t.Fatalf("lost sync error %q", env.LastError)
	}
	s.noteChange()
	env, err = s.liveStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if env.LastError != "sync boom" {
		t.Fatalf("noteChange wiped sync error %q", env.LastError)
	}
	if env.BodyCoverage.Total != 1 {
		t.Fatalf("coverage %+v", env.BodyCoverage)
	}
}

func TestStatusOverlaySurvivesBlockedRefresh(t *testing.T) {
	s, db := testServer(t)
	seedStatus(t, s)
	release := holdDuckPool(t, db)
	startStatus(t, s)
	s.endSync(errors.New("sync boom"))
	env, err := s.liveStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if env.LastError != "sync boom" {
		release()
		t.Fatalf("blocked refresh hid sync error %q", env.LastError)
	}
	st := req(t, s.Handler(), http.MethodGet, "/api/status")
	if st.Code != 200 {
		release()
		t.Fatalf("status %d", st.Code)
	}
	var got query.Envelope
	if err := json.Unmarshal(st.Body.Bytes(), &got); err != nil {
		release()
		t.Fatal(err)
	}
	if got.LastError != "sync boom" {
		release()
		t.Fatalf("handler hid sync error %q", got.LastError)
	}
	release()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		env, _ = s.liveStatus(context.Background())
		if env.LastError == "sync boom" && env.BodyCoverage.Total == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("overlay lost after refresh %+v", env)
}

func TestStatusJSONIncludesIndexState(t *testing.T) {
	s, _ := testServer(t)
	seedStatus(t, s)
	w := req(t, s.Handler(), http.MethodGet, "/api/status")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var env query.Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.SearchIndexState == "" {
		t.Fatal("missing search_index_state")
	}
	if env.StatusAsOf == "" {
		t.Fatal("missing status_as_of")
	}
}
