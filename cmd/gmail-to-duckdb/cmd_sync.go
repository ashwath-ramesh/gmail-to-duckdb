package main

import (
	"context"
	"fmt"
	"io"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/auth"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/gmail"
	mailsync "github.com/ashwath-ramesh/gmail-to-duckdb/internal/sync"
)

func cmdSync(args []string, stdout, stderr io.Writer) error {
	cf := globalFlags("sync")
	full := cf.fs.Bool("full", false, "list mailbox and mark missing as deleted")
	bodies := cf.fs.Bool("bodies", false, "fetch bodies for messages that lack them")
	if err := cf.parse(args); err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	opt := mailsync.Options{Full: *full, Bodies: *bodies}

	c, db, err := connect(*cf.db)
	if err != nil {
		return err
	}
	if c != nil {
		_, err = c.SyncNow(ctx, opt)
		return err
	}
	defer db.Close()
	api, err := gmailAPI(ctx, *cf.creds, *cf.db, *cf.oauthPort)
	if err != nil {
		return err
	}
	r := &mailsync.Runner{
		DB:  db,
		API: api,
		Log: func(format string, a ...any) { writeDiag(stderr, fmt.Sprintf(format, a...)) },
	}
	return r.Sync(ctx, opt)
}

// gmailAPI signs in (on first use) and returns a Gmail API client.
func gmailAPI(ctx context.Context, creds, dbPath string, oauthPort int) (*gmail.Client, error) {
	hc, err := auth.HTTPClient(ctx, creds, auth.TokenPath(dbPath), oauthPort)
	if err != nil {
		return nil, err
	}
	return gmail.New(ctx, hc)
}
