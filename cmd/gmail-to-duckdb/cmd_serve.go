package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/openurl"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	mailsync "github.com/ashwath-ramesh/gmail-to-duckdb/internal/sync"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/web"
)

func cmdServe(args []string, asServe bool, stdout, stderr io.Writer) error {
	cf := globalFlags("serve")
	port := cf.fs.String("port", cf.cfg.Port, "local port")
	every := cf.fs.String("sync-every", "", "incremental sync interval, at least 1m (e.g. 5m)")
	noBrowser := cf.fs.Bool("no-browser", false, "print the sign-in and mail page links, do not open a browser")
	if err := cf.parse(args); err != nil {
		return err
	}
	if !asServe {
		if c, err := web.Dial(*cf.db); err == nil {
			return openUI(stdout, stderr, *cf.db, !*noBrowser, func() (string, error) {
				return c.LoginLink(context.Background())
			})
		}
	}
	interval, err := syncInterval(*every)
	if err != nil {
		return err
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
	if api, err := gmailAPI(ctx, *cf.creds, *cf.db, *cf.oauthPort, !*noBrowser); err == nil {
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
	} else {
		s.NoSyncReason = "Gmail sign-in failed: " + err.Error()
		writeDiag(stderr, "sync is off: "+s.NoSyncReason)
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
	if err := openUI(stdout, stderr, *cf.db, !*noBrowser, func() (string, error) {
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

// minSyncEvery keeps a scheduled sync from using the Gmail quota all day.
const minSyncEvery = time.Minute

// syncInterval parses --sync-every. Empty or zero means no scheduled sync.
func syncInterval(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("sync-every: %w", err)
	}
	if d != 0 && d < minSyncEvery {
		return 0, fmt.Errorf("sync-every must be at least %s, not %s", minSyncEvery, s)
	}
	return d, nil
}

// openUI prints a one-time login link. With browser, it also opens the link
// through a private launcher file, so the code does not appear in process
// arguments.
func openUI(stdout, stderr io.Writer, dbPath string, browser bool, link func() (string, error)) error {
	l, err := link()
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "open the UI (this link works once, for 2 minutes):", l)
	if !browser {
		return nil
	}
	path, err := web.WriteLauncher(dbPath, l)
	if err != nil {
		return err
	}
	if err := openurl.Open(path); err != nil {
		writeDiag(stderr, fmt.Sprintf("could not open a browser: %v", err))
	}
	return nil
}
