package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := Load()
	if c.DB != DefaultDB || c.Credentials != DefaultCredentials {
		t.Fatalf("%+v", c)
	}
}

func TestWriteAndLoad(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	want := Config{DB: "/tmp/mail.duckdb", Credentials: "/tmp/credentials.json", OAuthPort: 9, Port: "9090"}
	if err := Write(want); err != nil {
		t.Fatal(err)
	}
	assertPrivateFile(t, Path())
	got := Load()
	if got.DB != want.DB || got.Credentials != want.Credentials || got.OAuthPort != 9 || got.Port != "9090" {
		t.Fatalf("%+v", got)
	}
}

func TestWriteReplacesPermissiveFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(`{"db":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makePermissive(Path()); err != nil {
		t.Fatal(err)
	}
	want := Config{DB: "/tmp/mail.duckdb", Credentials: "/tmp/credentials.json", OAuthPort: 9, Port: "9090"}
	if err := Write(want); err != nil {
		t.Fatal(err)
	}
	assertPrivateFile(t, Path())
	got := Load()
	if got.DB != want.DB {
		t.Fatalf("%+v", got)
	}
}

func TestExpand(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got := Expand("~/mail.duckdb"); got != filepath.Join(home, "mail.duckdb") {
		t.Fatalf("%s", got)
	}
}
