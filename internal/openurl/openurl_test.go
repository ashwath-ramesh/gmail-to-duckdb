package openurl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandNeverUsesShell(t *testing.T) {
	u := "https://accounts.google.com/o/oauth2/auth?a=1&b=2&calc"
	for goos, want := range map[string][]string{
		"darwin":  {"open", u},
		"windows": {"rundll32", "url.dll,FileProtocolHandler", u},
		"linux":   {"xdg-open", u},
	} {
		got := command(goos, u).Args
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("%s args %#v", goos, got)
		}
	}
}

func TestAllowed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "open.html")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{
		"https://accounts.google.com/o/oauth2/auth?a=1&b=2",
		"http://127.0.0.1:8080/",
		"http://localhost:8080/",
		"http://[::1]:8080/",
		file,
	} {
		if err := Allowed(ok); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://evil.example/",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"ms-settings:",
		"calc.exe",
		"https://user@evil.example/",
		filepath.Join(t.TempDir(), "missing.html"),
		t.TempDir(),
		"",
	} {
		if err := Allowed(bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}
