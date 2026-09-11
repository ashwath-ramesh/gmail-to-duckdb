package web

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
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
	syncErr   string
	env       query.Envelope
	hasCache  bool
	wake      chan struct{}
	stop      context.CancelFunc
	wg        sync.WaitGroup
	started   bool
}

func (s *Server) SetProgress(p mailsync.Progress) {
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	s.rt.phase = p.Phase
	s.rt.processed = p.Processed
}

func (s *Server) SeedStatus(ctx context.Context) {
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}
	env, err := query.Status(ctx, s.DB)
	if err != nil {
		return
	}
	s.rt.mu.Lock()
	s.rt.env = env
	s.rt.hasCache = true
	s.rt.mu.Unlock()
}

func (s *Server) StartStatus(ctx context.Context) {
	s.rt.mu.Lock()
	if s.rt.started {
		s.rt.mu.Unlock()
		return
	}
	s.rt.started = true
	if s.rt.wake == nil {
		s.rt.wake = make(chan struct{}, 1)
	}
	ctx, cancel := context.WithCancel(ctx)
	s.rt.stop = cancel
	s.rt.wg.Add(1)
	s.rt.mu.Unlock()
	go s.statusLoop(ctx)
}

func (s *Server) StopStatus() {
	s.rt.mu.Lock()
	stop := s.rt.stop
	s.rt.mu.Unlock()
	if stop != nil {
		stop()
	}
	s.rt.wg.Wait()
}

func (s *Server) statusLoop(ctx context.Context) {
	defer s.rt.wg.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	s.refreshStatus(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.rt.wake:
		case <-tick.C:
		}
		s.refreshStatus(ctx)
	}
}

func (s *Server) wakeStatus() {
	s.rt.mu.Lock()
	wake := s.rt.wake
	s.rt.mu.Unlock()
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (s *Server) noteChange() {
	s.wakeStatus()
}

func (s *Server) refreshStatus(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}
	env, err := query.Status(ctx, s.DB)
	if err != nil {
		return
	}
	s.rt.mu.Lock()
	s.rt.env = env
	s.rt.hasCache = true
	s.rt.mu.Unlock()
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
	s.rt.syncErr = ""
	return nil
}

func (s *Server) endSync(err error) {
	s.rt.mu.Lock()
	s.rt.running = false
	s.rt.phase = "idle"
	if err != nil {
		s.rt.syncErr = err.Error()
	} else {
		s.rt.syncErr = ""
	}
	s.rt.mu.Unlock()
	s.wakeStatus()
}

func (s *Server) doSync(ctx context.Context, opt mailsync.Options) error {
	return s.Sync(ctx, opt)
}

func (s *Server) syncContext() context.Context {
	if s.SyncCtx != nil {
		return s.SyncCtx
	}
	return context.Background()
}

func (s *Server) liveStatus(ctx context.Context) (query.Envelope, error) {
	_ = ctx
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	env := query.Envelope{
		SchemaVersion: store.SchemaVersion,
		DuckDBUI:      s.AllowDuckUI,
		Phase:         s.rt.phase,
		Processed:     s.rt.processed,
	}
	if !s.rt.hasCache {
		if env.Phase == "" {
			env.Phase = "initializing"
		}
		if s.rt.syncErr != "" {
			env.LastError = s.rt.syncErr
		}
		return env, nil
	}
	env = s.rt.env
	env.DuckDBUI = s.AllowDuckUI
	env.Phase = s.rt.phase
	env.Processed = s.rt.processed
	if env.Phase == "" {
		if s.rt.running {
			env.Phase = "running"
		} else {
			env.Phase = "idle"
		}
	}
	if s.rt.syncErr != "" {
		env.LastError = s.rt.syncErr
	}
	return env, nil
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
