//go:build windows

package config

import (
	"fmt"
	"os/exec"
	"testing"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
)

func assertPrivateFile(t *testing.T, path string) {
	t.Helper()
	if err := privfile.Check(path); err != nil {
		t.Fatal(err)
	}
}

func makePermissive(path string) error {
	out, err := exec.Command("icacls", path, "/grant", "Everyone:R").CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls: %v: %s", err, out)
	}
	return nil
}
