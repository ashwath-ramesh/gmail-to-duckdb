package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/auth"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/config"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/gmail"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/openurl"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	mailsync "github.com/ashwath-ramesh/gmail-to-duckdb/internal/sync"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/web"
)

func cmdServe(args []string, asServe bool, stdout, stderr io.Writer) error {
	cf := globalFlags("serve")
	cfg := config.Load()
	port := cf.fs.String("port", cfg.Port, "local port")
	every := cf.fs.String("sync-every", "", "incremental sync interval (e.g. 5m)")
	if err := parseFlags(cf.fs, args); err != nil {
		return err
	}
	if !asServe {
		if c, err := web.Dial(*cf.db); err == nil {
			return openUI(stdout, stderr, *cf.db, func() (string, error) {
				return c.LoginLink(context.Background())
			})
		}
	}
	var interval time.Duration
	if *every != "" {
		d, err := time.ParseDuration(*every)
		if err != nil {
			return fmt.Errorf("sync-every: %w", err)
		}
		interval = d
	}
	ctx, stop := signalContext()
	defer stop()

	db, err := store.Open(*cf.db)
	if err != nil {
		return err
	}
	defer db.Close()
	db.IndexLog = func(format string, a ...any) { writeDiag(stderr, fmt.Sprintf(format, a...)) }

	tok, err := web.NewToken()
	if err != nil {
		return err
	}
	s := &web.Server{DB: db, Token: tok, SyncCtx: ctx}
	// Runs before db.Close: stop the work, then wait for a running sync to end.
	defer func() {
		stop()
		s.WaitIdle()
	}()
	if hc, err := auth.HTTPClient(ctx, *cf.creds, auth.TokenPath(*cf.db), *cf.oauthPort); err == nil {
		if api, err := gmail.New(ctx, hc); err == nil {
			s.FetchBody = func(ctx context.Context, id string) error {
				return mailsync.FetchOnDemand(ctx, db, api, id)
			}
			s.Sync = func(ctx context.Context, opt mailsync.Options) error {
				r := &mailsync.Runner{
					DB:         db,
					API:        api,
					Log:        func(format string, a ...any) { writeDiag(stderr, fmt.Sprintf(format, a...)) },
					OnProgress: s.SetProgress,
				}
				return r.Sync(ctx, opt)
			}
		}
	} else {
		writeDiag(stderr, fmt.Sprintf("gmail client disabled: %v", err))
	}

	ln, err := web.Listen(*port)
	if err != nil {
		return err
	}
	listenURL, h, err := s.BindListener(ln)
	if err != nil {
		_ = ln.Close()
		return err
	}
	errc := make(chan error, 1)
	go func() { errc <- web.Serve(ctx, ln, h) }()
	if err := web.WriteServeFile(*cf.db, web.ServeInfo{URL: listenURL, Token: s.Token, PID: os.Getpid()}); err != nil {
		stop()
		return err
	}
	defer func() { _ = web.RemoveServeFile(*cf.db) }()
	defer func() { _ = web.RemoveLauncher(*cf.db) }()
	fmt.Fprintln(stdout, "listening on", listenURL)
	if err := openUI(stdout, stderr, *cf.db, func() (string, error) {
		code, err := s.NewLoginCode()
		return web.LoginLink(listenURL, code), err
	}); err != nil {
		writeDiag(stderr, fmt.Sprintf("open UI: %v", err))
	}
	s.StartStatus(ctx)
	defer s.StopStatus()
	db.StartMaintenance(ctx)

	startup := asServe || interval > 0
	if startup && s.Sync != nil {
		if err := s.StartSync(ctx, mailsync.Options{}); err != nil {
			writeDiag(stderr, fmt.Sprintf("startup sync: %v", err))
		}
	}
	if interval > 0 && s.Sync != nil {
		go func() {
			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if err := s.StartSync(ctx, mailsync.Options{}); err != nil {
						writeDiag(stderr, fmt.Sprintf("scheduled sync: %v", err))
					}
				}
			}
		}()
	}

	return <-errc
}

// openUI prints a one-time login link and opens it in the browser through a
// private launcher file, so the code does not appear in process arguments.
func openUI(stdout, stderr io.Writer, dbPath string, link func() (string, error)) error {
	l, err := link()
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "open the UI (this link works once, for 2 minutes):", l)
	path, err := web.WriteLauncher(dbPath, l)
	if err != nil {
		return err
	}
	if err := openurl.Open(path); err != nil {
		writeDiag(stderr, fmt.Sprintf("could not open a browser: %v", err))
	}
	return nil
}
