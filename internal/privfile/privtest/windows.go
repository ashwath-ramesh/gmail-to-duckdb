//go:build windows

package privtest

import (
	"fmt"
	"os/exec"
	"testing"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
)

// AssertPrivate fails unless path passes the private-file ACL check.
func AssertPrivate(t *testing.T, path string) {
	t.Helper()
	if err := privfile.Check(path); err != nil {
		t.Fatal(err)
	}
}

// MakePermissive lets Everyone read path.
func MakePermissive(path string) error {
	out, err := exec.Command("icacls", path, "/grant", "Everyone:R").CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls: %v: %s", err, out)
	}
	return nil
}

// MakeDirNotCreatable stops new files in dir.
func MakeDirNotCreatable(dir string) error {
	out, err := exec.Command("icacls", dir, "/deny", "Everyone:(WD,AD)").CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls deny: %v: %s", err, out)
	}
	return nil
}

// RestoreDirCreatable undoes MakeDirNotCreatable.
func RestoreDirCreatable(dir string) error {
	out, err := exec.Command("icacls", dir, "/remove:d", "Everyone").CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls restore: %v: %s", err, out)
	}
	return nil
}
