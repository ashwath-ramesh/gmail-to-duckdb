package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
)

var walSuffixes = []string{".wal", ".wal.checkpoint", ".wal.recovery"}

func prepareOpen(path string) (string, string, error) {
	if err := rejectPathSyntax(path); err != nil {
		return "", "", err
	}
	path = filepath.Clean(path)
	if path == "" || path == "." {
		return "", "", fmt.Errorf("empty database path")
	}
	if err := rejectMemoryPath(path); err != nil {
		return "", "", err
	}
	privfile.LockDownProcess()
	if err := prepareParent(filepath.Dir(path)); err != nil {
		return "", "", err
	}
	if err := hardenExisting(path); err != nil {
		return "", "", err
	}
	spill, err := prepareSpill(path)
	if err != nil {
		return "", "", err
	}
	return path, spill, nil
}

func prepareParent(dir string) error {
	if dir == "" {
		return fmt.Errorf("empty database directory")
	}
	fi, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return privfile.MkdirPrivate(dir)
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("database directory is a symlink")
	}
	if !fi.IsDir() {
		return fmt.Errorf("database parent is not a directory")
	}
	return checkExistingParent(dir)
}

func hardenExisting(path string) error {
	if err := hardenIfExists(path); err != nil {
		return err
	}
	for _, suffix := range walSuffixes {
		if err := hardenIfExists(path + suffix); err != nil {
			return fmt.Errorf("wal sidecar: %w", err)
		}
	}
	return nil
}

func hardenIfExists(path string) error {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path is a symlink")
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file")
	}
	return privfile.Harden(path)
}

func prepareSpill(path string) (string, error) {
	dir := path + ".tmp"
	fi, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		if err := privfile.MkdirPrivate(dir); err != nil {
			return "", err
		}
		return dir, nil
	}
	if err != nil {
		return "", err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("spill directory is a symlink")
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("spill path is not a directory")
	}
	if err := privfile.HardenDir(dir); err != nil {
		return "", err
	}
	if err := hardenSpillContents(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func rejectPathSyntax(path string) error {
	if strings.IndexByte(path, 0) >= 0 {
		return fmt.Errorf("database path contains a NUL byte")
	}
	if strings.Contains(path, "?") {
		return fmt.Errorf("database path is a filesystem path; DSN options after '?' are not supported")
	}
	return nil
}

func rejectMemoryPath(path string) error {
	base := strings.ToLower(strings.TrimSpace(path))
	if base == ":memory:" || strings.HasPrefix(base, ":memory:") {
		return fmt.Errorf("in-memory DuckDB URLs are not supported")
	}
	return nil
}

func hardenSpillContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("spill entry is a symlink")
		}
		if fi.IsDir() {
			if err := privfile.HardenDir(p); err != nil {
				return err
			}
			if err := hardenSpillContents(p); err != nil {
				return err
			}
			continue
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("spill entry is not a regular file")
		}
		if err := privfile.Harden(p); err != nil {
			return err
		}
	}
	return nil
}
