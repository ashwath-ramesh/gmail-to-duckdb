package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTokenPath(t *testing.T) {
	if got := TokenPath("mail.duckdb"); got != "mail.token.json" {
		t.Fatalf("got %s", got)
	}
	if got := TokenPath("/tmp/data/box.duckdb"); got != "/tmp/data/box.token.json" {
		t.Fatalf("got %s", got)
	}
}

func TestCodeFromRedirect(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"http://127.0.0.1:41807/?state=state&iss=https://accounts.google.com&code=4/0ATsMZqA", "4/0ATsMZqA"},
		{"http://127.0.0.1:41609/?code=abc&state=state", "abc"},
		{"4/0ATsMZqA", "4/0ATsMZqA"},
		{"  4/xyz  ", "4/xyz"},
	}
	for _, c := range cases {
		got, err := CodeFromRedirect(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("%q: got %q want %q", c.in, got, c.want)
		}
	}
	if _, err := CodeFromRedirect("http://127.0.0.1:41807/?state=state"); err == nil {
		t.Fatal("expected error")
	}
}

func TestWriteTokenMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mail.token.json")
	if err := writeFile0600(path, []byte(`{"access_token":"x"}`)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
}
