package auth

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func skipIfNoSymlink(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if runtime.GOOS == "windows" {
		t.Skipf("symlink: %v", err)
	}
	t.Fatal(err)
}

func assertNoTempFiles(t *testing.T, dir string) {
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
