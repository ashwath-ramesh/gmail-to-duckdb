//go:build windows

package store

import (
	"fmt"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
)

func checkExistingParent(dir string) error {
	if err := privfile.CheckDir(dir); err == nil {
		return nil
	}
	return fmt.Errorf("database directory %s is not private; use a directory that allows only the current user and SYSTEM, or run init so the default data directory is created private. This tool does not change ACLs on a custom directory", dir)
}
