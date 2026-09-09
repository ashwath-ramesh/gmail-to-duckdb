package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/auth"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/config"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/gmail"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/openurl"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/parse"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	mailsync "github.com/ashwath-ramesh/gmail-to-duckdb/internal/sync"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/web"
)

func cmdServe(args []string, asServe bool, stdout, stderr io.Writer) error {
	cf := globalFlags("serve")
	cfg := config.Load()
	port := cf.fs.String("port", cfg.Port, "local port")
	every := cf.fs.String("sync-every", "", "incremental sync interval (e.g. 5m)")
	duckUI := cf.fs.Bool("duckdb-ui", false, "allow unauthenticated DuckDB UI on loopback")
	if err := parseFlags(cf.fs, args); err != nil {
		return err
	}
	var interval time.Duration
	if *every != "" {
		d, err := time.ParseDuration(*every)
		if err != nil {
			return fmt.Errorf("sync-every: %w", err)
		}
		interval = d
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	db, err := store.OpenWith(*cf.db, store.Options{DuckUI: *duckUI})
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.EnsureFTS(ctx); err != nil {
		writeDiag(stderr, fmt.Sprintf("fts: %v", err))
	}

	tok, err := web.NewToken()
	if err != nil {
		return err
	}
	s := &web.Server{DB: db, Token: tok, SyncCtx: ctx, AllowDuckUI: *duckUI}
	if hc, err := auth.HTTPClient(ctx, *cf.creds, auth.TokenPath(*cf.db), *cf.oauthPort); err == nil {
		if api, err := gmail.New(ctx, hc); err == nil {
			s.FetchBody = func(ctx context.Context, id string) (string, error) {
				raw, err := api.Get(ctx, id, "full")
				if err != nil {
					return "", err
				}
				msg, err := parse.Message(raw, "")
				if err != nil {
					return "", err
				}
				return msg.Body, nil
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

	fmt.Fprintln(stdout, "listening on", listenURL)
	_ = openurl.Open(listenURL)
	return <-errc
}
