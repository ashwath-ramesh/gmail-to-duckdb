package setup

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/auth"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/config"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/web"
)

func TestInitAndDoctor(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	src := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(src, []byte(`{"installed":{"client_id":"x","client_secret":"y"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Init(src, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.Credentials); err != nil {
		t.Fatal(err)
	}
	got := config.Load()
	if got.Credentials != cfg.Credentials {
		t.Fatalf("%+v", got)
	}
	env := Doctor(context.Background(), cfg)
	if DoctorOK(env) {
		t.Fatal("doctor should fail before token and db exist")
	}
	var names []string
	for _, c := range env.Checks {
		names = append(names, c.Name)
		if c.Name == "credentials" && !c.OK {
			t.Fatalf("credentials: %+v", c)
		}
	}
	if len(names) < 5 {
		t.Fatalf("checks %#v", names)
	}
}

func TestInitHardensExistingDataDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := config.DataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(src, []byte(`{"installed":{"client_id":"x","client_secret":"y"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(src, ""); err != nil {
		t.Fatal(err)
	}
	if err := privfile.CheckDir(dir); err != nil {
		t.Fatalf("init did not harden data dir: %v", err)
	}
}

func TestInitRejectsWebClient(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	src := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(src, []byte(`{"web":{"client_id":"x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(src, ""); err == nil {
		t.Fatal("expected desktop client error")
	}
}

func TestInitDoesNotChangeSourceFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	src := filepath.Join(t.TempDir(), "credentials.json")
	raw := []byte(`{"installed":{"client_id":"x","client_secret":"y"}}`)
	if err := os.WriteFile(src, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Init(src, ""); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode().Perm() != after.Mode().Perm() {
		t.Fatalf("source mode changed %o -> %o", before.Mode().Perm(), after.Mode().Perm())
	}
	got, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatal("source bytes changed")
	}
}

func TestInitReplacesPermissiveCopiedCredentials(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dest := filepath.Join(config.Dir(), "credentials.json")
	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte(`{"installed":{"client_id":"old"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makePermissive(dest); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(src, []byte(`{"installed":{"client_id":"x","client_secret":"y"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Init(src, "")
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateFile(t, cfg.Credentials)
	got, err := os.ReadFile(cfg.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"x"`) {
		t.Fatalf("copy missing: %s", got)
	}
}

func TestDoctorRejectsPermissiveToken(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	src := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(src, []byte(`{"installed":{"client_id":"x","client_secret":"y"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Init(src, "")
	if err != nil {
		t.Fatal(err)
	}
	tok := `{"refresh_token":"r","scope":"https://www.googleapis.com/auth/gmail.readonly"}`
	path := auth.TokenPath(cfg.DB)
	if err := os.WriteFile(path, []byte(tok), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makePermissive(path); err != nil {
		t.Fatal(err)
	}
	env := Doctor(context.Background(), cfg)
	for _, c := range env.Checks {
		if c.Name == "token" {
			if c.OK {
				t.Fatalf("permissive token passed: %+v", c)
			}
			return
		}
	}
	t.Fatal("missing token check")
}

func TestDoctorAcceptsPrivateToken(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	src := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(src, []byte(`{"installed":{"client_id":"x","client_secret":"y"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Init(src, "")
	if err != nil {
		t.Fatal(err)
	}
	tok := []byte(`{"refresh_token":"r","scope":"https://www.googleapis.com/auth/gmail.readonly"}`)
	if err := privfile.Write(auth.TokenPath(cfg.DB), tok); err != nil {
		t.Fatal(err)
	}
	env := Doctor(context.Background(), cfg)
	for _, c := range env.Checks {
		if c.Name == "token" {
			if !c.OK {
				t.Fatalf("private token failed: %+v", c)
			}
			return
		}
	}
	t.Fatal("missing token check")
}

func TestDoctorStaleServeDoesNotOpenDB(t *testing.T) {
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		dir = filepath.Join(dir, "db")
		if err := privfile.MkdirPrivate(dir); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "mail.duckdb")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(web.ServePath(dbPath), []byte(`{"url":"http://127.0.0.1:1","token":"x","pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := Doctor(context.Background(), config.Config{DB: dbPath, Port: "9090", OAuthPort: 41807})
	var dbCheck bool
	for _, c := range env.Checks {
		if c.Name == "database" {
			dbCheck = true
			if c.OK || !strings.Contains(c.Detail, "not reachable") {
				t.Fatalf("%+v", c)
			}
		}
		if c.Name == "ui_port" && !strings.Contains(c.Detail, ":1") && !strings.Contains(c.Detail, "1") {
			// serve file port 1 overrides 9090
		}
	}
	if !dbCheck {
		t.Fatal("missing database check")
	}
}

func TestDoctorUnreadableServeDoesNotOpenDB(t *testing.T) {
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		dir = filepath.Join(dir, "db")
		if err := privfile.MkdirPrivate(dir); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "mail.duckdb")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(web.ServePath(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	env := Doctor(context.Background(), config.Config{DB: dbPath, Port: "9090", OAuthPort: 41807})
	for _, c := range env.Checks {
		if c.Name == "database" {
			if c.OK || !strings.Contains(c.Detail, "unreadable") {
				t.Fatalf("%+v", c)
			}
			return
		}
	}
	t.Fatal("missing database check")
}
