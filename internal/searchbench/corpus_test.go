package searchbench

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMailboxPath(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		t.Setenv("GMAIL_BENCH_FIXTURE", "")
		p, provided, err := MailboxPath()
		if err != nil || provided || p != "" {
			t.Fatalf("path=%q provided=%t err=%v", p, provided, err)
		}
	})
	t.Run("new duckdb file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bench.duckdb")
		t.Setenv("GMAIL_BENCH_FIXTURE", path)
		got, provided, err := MailboxPath()
		if err != nil || !provided || got != path {
			t.Fatalf("path=%q provided=%t err=%v", got, provided, err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("created file: %v", err)
		}
	})
	t.Run("existing dir absent file", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("GMAIL_BENCH_FIXTURE", dir)
		want := filepath.Join(dir, "bench.duckdb")
		got, provided, err := MailboxPath()
		if err != nil || !provided || got != want {
			t.Fatalf("path=%q provided=%t err=%v", got, provided, err)
		}
		if _, err := os.Stat(want); !os.IsNotExist(err) {
			t.Fatalf("created file: %v", err)
		}
	})
	t.Run("existing file unchanged", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "mail.duckdb")
		if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GMAIL_BENCH_FIXTURE", path)
		got, provided, err := MailboxPath()
		if err != nil || !provided || got != path {
			t.Fatalf("path=%q provided=%t err=%v", got, provided, err)
		}
		b, err := os.ReadFile(path)
		if err != nil || string(b) != "keep" {
			t.Fatalf("reset file %q %v", b, err)
		}
	})
	t.Run("stat error", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "notdir")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Child of a regular file: Unix Stat is ENOTDIR; Windows Stat is
		// not-exist. A missing .duckdb path is accepted by MailboxPath, so
		// use a non-.duckdb child that both platforms reject.
		t.Setenv("GMAIL_BENCH_FIXTURE", filepath.Join(file, "child"))
		_, provided, err := MailboxPath()
		if err == nil || !provided {
			t.Fatalf("provided=%t err=%v", provided, err)
		}
	})
}
