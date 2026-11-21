//go:build unix

package privtest

import (
	"os"
	"testing"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
)

// AssertPrivate fails unless path is a regular file with mode 0600 that the
// current user owns.
func AssertPrivate(t *testing.T, path string) {
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

// MakePermissive lets other users read path.
func MakePermissive(path string) error {
	return os.Chmod(path, 0o644)
}

// MakeDirNotCreatable stops new files in dir.
func MakeDirNotCreatable(dir string) error {
	return os.Chmod(dir, 0o555)
}

// RestoreDirCreatable undoes MakeDirNotCreatable.
func RestoreDirCreatable(dir string) error {
	return os.Chmod(dir, 0o700)
}
