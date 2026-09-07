package main

import (
	"fmt"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/web"
)

func withAccess(dbPath string, fn func(query.Access) error) error {
	if _, err := web.ReadServeFile(dbPath); err == nil {
		c, err := web.Dial(dbPath)
		if err != nil {
			return fmt.Errorf("serve is marked running but not reachable: %w", err)
		}
		defer c.Close()
		return fn(c)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	return fn(query.Local{DB: db})
}
