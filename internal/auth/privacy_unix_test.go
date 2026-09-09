//go:build unix

package auth

import (
	"os"
	"testing"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
)

func assertPrivateFile(t *testing.T, path string) {
	t.Helper()
	if err := privfile.Check(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("not a regular file: %s", info.Mode())
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
}

func makePermissive(path string) error {
	return os.Chmod(path, 0o644)
}

func makeDirNotCreatable(dir string) error {
	return os.Chmod(dir, 0o555)
}

func restoreDirCreatable(dir string) error {
	return os.Chmod(dir, 0o700)
}
