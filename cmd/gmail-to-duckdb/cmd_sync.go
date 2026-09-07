package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/auth"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/gmail"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	mailsync "github.com/ashwath-ramesh/gmail-to-duckdb/internal/sync"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/web"
)

func cmdSync(args []string, stdout, stderr io.Writer) error {
	cf := globalFlags("sync")
	full := cf.fs.Bool("full", false, "list mailbox and mark missing as deleted")
	bodies := cf.fs.Bool("bodies", false, "fetch bodies for messages that lack them")
	if err := parseFlags(cf.fs, args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	opt := mailsync.Options{Full: *full, Bodies: *bodies}

	if _, err := web.ReadServeFile(*cf.db); err == nil {
		c, err := web.Dial(*cf.db)
		if err != nil {
			return fmt.Errorf("serve is marked running but not reachable: %w", err)
		}
		defer c.Close()
		_, err = c.SyncNow(ctx, opt)
		return err
	}

	db, err := store.Open(*cf.db)
	if err != nil {
		return err
	}
	defer db.Close()
	hc, err := auth.HTTPClient(ctx, *cf.creds, auth.TokenPath(*cf.db), *cf.oauthPort)
	if err != nil {
		return err
	}
	api, err := gmail.New(ctx, hc)
	if err != nil {
		return err
	}
	r := &mailsync.Runner{
		DB:  db,
		API: api,
		Log: func(format string, a ...any) { fmt.Fprintf(stderr, format+"\n", a...) },
	}
	return r.Sync(ctx, opt)
}
