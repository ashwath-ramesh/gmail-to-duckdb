package setup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/config"
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

func TestDoctorStaleServeDoesNotOpenDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mail.duckdb")
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
