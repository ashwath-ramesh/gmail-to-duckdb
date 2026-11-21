// Package privtest has test helpers for private files. Only tests import it.
package privtest

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// SkipIfNoSymlink skips the test on Windows when creating a symlink failed,
// because that needs a privilege. On other systems the error fails the test.
func SkipIfNoSymlink(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if runtime.GOOS == "windows" {
		t.Skipf("symlink: %v", err)
	}
	t.Fatal(err)
}

// AssertNoTempFiles fails if an atomic write left a temp file in dir.
func AssertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Fatalf("temp left: %s", e.Name())
		}
	}
}
