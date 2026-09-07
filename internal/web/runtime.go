package web

import (
	"context"
	"errors"
	"sync"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
	mailsync "github.com/ashwath-ramesh/gmail-to-duckdb/internal/sync"
)

var (
	errSyncBusy = errors.New("sync already running")
	errNoSync   = errors.New("sync is not configured; pass --credentials")
)

type runtime struct {
	mu        sync.Mutex
	running   bool
	phase     string
	processed int
	lastErr   string
	lastOK    string
	coverage  query.BodyCoverage
	schemaVer int
	fts       bool
	cached    bool
}

func (s *Server) SetProgress(p mailsync.Progress) {
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	s.rt.phase = p.Phase
	s.rt.processed = p.Processed
}

func (s *Server) cacheFrom(env query.Envelope) {
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	s.rt.cached = true
	s.rt.schemaVer = env.SchemaVersion
	s.rt.lastOK = env.LastSync
	s.rt.lastErr = env.LastError
	s.rt.coverage = env.BodyCoverage
	s.rt.fts = env.FTS
}

func (s *Server) beginSync() error {
	if s.Sync == nil {
		return errNoSync
	}
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	if s.rt.running {
		return errSyncBusy
	}
	s.rt.running = true
	s.rt.phase = "starting"
	s.rt.lastErr = ""
	return nil
}

func (s *Server) endSync(err error) {
	s.rt.mu.Lock()
	s.rt.running = false
	s.rt.phase = "idle"
	if err != nil {
		s.rt.lastErr = err.Error()
	}
	s.rt.mu.Unlock()
	if env, e := query.Status(context.Background(), s.DB); e == nil {
		s.cacheFrom(env)
	}
	if err != nil {
		s.rt.mu.Lock()
		s.rt.lastErr = err.Error()
		s.rt.mu.Unlock()
	}
}

func (s *Server) doSync(ctx context.Context, opt mailsync.Options) error {
	if env, err := query.Status(ctx, s.DB); err == nil {
		s.cacheFrom(env)
	}
	return s.Sync(ctx, opt)
}

func (s *Server) syncContext() context.Context {
	if s.SyncCtx != nil {
		return s.SyncCtx
	}
	return context.Background()
}

func (s *Server) liveStatus(ctx context.Context) (query.Envelope, error) {
	s.rt.mu.Lock()
	running := s.rt.running
	env := query.Envelope{
		SchemaVersion: s.rt.schemaVer,
		LastSync:      s.rt.lastOK,
		LastError:     s.rt.lastErr,
		BodyCoverage:  s.rt.coverage,
		Phase:         s.rt.phase,
		Processed:     s.rt.processed,
		FTS:           s.rt.fts,
	}
	cached := s.rt.cached
	s.rt.mu.Unlock()
	if running && cached {
		if env.Phase == "" {
			env.Phase = "running"
		}
		env.DuckDBUI = s.AllowDuckUI
		return env, nil
	}
	out, err := query.Status(ctx, s.DB)
	if err != nil {
		return query.Envelope{}, err
	}
	if env.Phase != "" {
		out.Phase = env.Phase
		out.Processed = env.Processed
	}
	if out.Phase == "" {
		out.Phase = "idle"
	}
	if env.LastError != "" && out.LastError == "" {
		out.LastError = env.LastError
	}
	out.DuckDBUI = s.AllowDuckUI
	return out, nil
}

func (s *Server) StartSync(ctx context.Context, opt mailsync.Options) error {
	if err := s.beginSync(); err != nil {
		return err
	}
	go func() {
		err := s.doSync(ctx, opt)
		s.endSync(err)
	}()
	return nil
}

func (s *Server) WaitSync(ctx context.Context, opt mailsync.Options) error {
	if err := s.beginSync(); err != nil {
		return err
	}
	err := s.doSync(ctx, opt)
	s.endSync(err)
	return err
}
