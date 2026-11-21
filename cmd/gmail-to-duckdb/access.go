package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/web"
)

// signalContext stops on Ctrl+C, on kill (SIGTERM), and when the terminal
// closes (SIGHUP), so serve always removes its serve file and closes DuckDB.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
}

func withAccess(dbPath string, fn func(query.Access) error) error {
	c, db, err := connect(dbPath)
	if err != nil {
		return err
	}
	if c != nil {
		return fn(c)
	}
	defer db.Close()
	return fn(query.Local{DB: db})
}

// connect returns a client for a running serve, or else opens the database.
// The DuckDB file lock decides if a serve that does not answer is still alive:
// if the open succeeds, that serve has stopped, and its serve file is stale.
func connect(dbPath string) (*web.Client, *store.DB, error) {
	info, err := web.ReadServeFile(dbPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("serve file present but unreadable: %w", err)
	}
	var dialErr error
	if err == nil {
		c, err := web.Dial(dbPath)
		if err == nil {
			return c, nil, nil
		}
		dialErr = err
	}
	db, err := store.Open(dbPath)
	if err != nil {
		if dialErr != nil {
			return nil, nil, fmt.Errorf("serve (pid %d) does not answer at %s (%v), and it still holds the database: %w; stop that process, then try again", info.PID, info.URL, dialErr, err)
		}
		return nil, nil, err
	}
	if dialErr != nil && !web.IsOtherDatabase(dialErr) {
		if err := web.RemoveServeFile(dbPath); err != nil {
			_ = db.Close()
			return nil, nil, fmt.Errorf("remove stale serve file: %w", err)
		}
		writeDiag(os.Stderr, "removed a stale serve file from a serve that stopped")
	}
	return nil, db, nil
}
