package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
)

func TestOpenRejectsDSNBeforeMutation(t *testing.T) {
	root := t.TempDir()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "newparent")
	base := filepath.Join(parent, "mail.duckdb")
	cases := []struct {
		name string
		path string
		want string
	}{
		{"query", base + "?access_mode=READ_ONLY", "?"},
		{"nul", base + "\x00sneak", "NUL"},
		{"memory", ":memory:", "memory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := Open(tc.path)
			if err == nil {
				_ = db.Close()
				t.Fatal("expected reject")
			}
			if db != nil {
				t.Fatal("db on failure")
			}
			if !strings.Contains(err.Error(), tc.want) && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Fatalf("want %q in %v", tc.want, err)
			}
			if _, err := os.Lstat(parent); !os.IsNotExist(err) {
				t.Fatal("created parent")
			}
			if _, err := os.Lstat(base); !os.IsNotExist(err) {
				t.Fatal("created database")
			}
			if _, err := os.Lstat(base + ".tmp"); !os.IsNotExist(err) {
				t.Fatal("created spill")
			}
			got, err := os.ReadFile(keep)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "keep" {
				t.Fatal("unrelated file changed")
			}
		})
	}
}

func TestOpenHardensExistingDBWhileOpen(t *testing.T) {
	path := filepath.Join(privateDir(t), "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makePermissiveFile(path); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := privfile.Check(path); err != nil {
		t.Fatalf("db not private while open: %v", err)
	}
}

func TestOpenRejectsSymlinkDB(t *testing.T) {
	dir := privateDir(t)
	target := filepath.Join(dir, "target.duckdb")
	path := filepath.Join(dir, "mail.duckdb")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink: %v", err)
		}
		t.Fatal(err)
	}
	db, err := Open(path)
	if err == nil {
		_ = db.Close()
		t.Fatal("expected symlink reject")
	}
	if db != nil {
		t.Fatal("db on failure")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep" {
		t.Fatal("target changed")
	}
}

func TestOpenKeepsPathStable(t *testing.T) {
	path := filepath.Join(privateDir(t), "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.Mode().IsRegular() {
		t.Fatal("db path relocated or not a file")
	}
	if err := privfile.Check(path); err != nil {
		t.Fatalf("db not private while open: %v", err)
	}
}

func TestOpenHardensExistingSpillContents(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "mail.duckdb")
	spill := path + ".tmp"
	if err := os.Mkdir(spill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(spill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := makePermissiveDir(spill); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(spill, "old.spill")
	if err := os.WriteFile(inside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makePermissiveFile(inside); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.secret")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makePermissiveFile(outside); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertDirPrivate(t, spill)
	if err := privfile.Check(inside); err != nil {
		t.Fatalf("spill file not private while open: %v", err)
	}
	if err := privfile.Check(outside); err == nil {
		t.Fatal("walked or chmodded a file outside the spill directory")
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep" {
		t.Fatal("file outside spill directory changed")
	}
}

func TestOpenRejectsSpillInnerSymlink(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "mail.duckdb")
	spill := path + ".tmp"
	if err := privfile.MkdirPrivate(spill); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "outside.secret")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(spill, "out.link")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink: %v", err)
		}
		t.Fatal(err)
	}
	db, err := Open(path)
	if err == nil {
		_ = db.Close()
		t.Fatal("expected inner spill symlink reject")
	}
	if db != nil {
		t.Fatal("db on failure")
	}
	if _, err := os.Lstat(filepath.Join(spill, "out.link")); err != nil {
		t.Fatal("spill symlink deleted")
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep" {
		t.Fatal("symlink target changed")
	}
}

func TestOpenRejectsSpillSymlink(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "mail.duckdb")
	target := filepath.Join(dir, "other")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(target, "keep")
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+".tmp"); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink: %v", err)
		}
		t.Fatal(err)
	}
	db, err := Open(path)
	if err == nil {
		_ = db.Close()
		t.Fatal("expected spill symlink reject")
	}
	if db != nil {
		t.Fatal("db on failure")
	}
	got, err := os.ReadFile(keep)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep" {
		t.Fatal("spill symlink target changed")
	}
}

func TestOpenRejectsWALSymlink(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.UpsertMessages(ctx, []Message{sample("s1", testAt())}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "wal.target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	wal := path + ".wal"
	_ = os.Remove(wal)
	if err := os.Symlink(target, wal); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink: %v", err)
		}
		t.Fatal(err)
	}
	db, err = Open(path)
	if err == nil {
		_ = db.Close()
		t.Fatal("expected wal symlink reject")
	}
	if db != nil {
		t.Fatal("db on failure")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep" {
		t.Fatal("wal symlink target changed")
	}
	if err := os.Remove(wal); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gotMsg, err := db.GetMessage(ctx, "s1")
	if err != nil || gotMsg.ID != "s1" {
		t.Fatalf("existing rows lost after failed open: %v %+v", err, gotMsg)
	}
}

func TestOpenDoesNotDeleteWALSidecars(t *testing.T) {
	path := filepath.Join(privateDir(t), "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.UpsertMessages(ctx, []Message{sample("w1", testAt())}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".wal.checkpoint", ".wal.recovery"} {
		p := path + suffix
		if err := os.WriteFile(p, []byte("sidecar"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := makePermissiveFile(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Lstat(path + ".wal"); err == nil {
		if err := os.Chmod(path+".wal", 0o644); err != nil {
			t.Fatal(err)
		}
		if err := makePermissiveFile(path + ".wal"); err != nil {
			t.Fatal(err)
		}
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, suffix := range []string{".wal", ".wal.checkpoint", ".wal.recovery"} {
		p := path + suffix
		if _, err := os.Lstat(p); err != nil {
			if suffix == ".wal" {
				continue
			}
			t.Fatalf("sidecar deleted: %s", suffix)
		}
		if err := privfile.Check(p); err != nil {
			t.Fatalf("%s not private while open: %v", suffix, err)
		}
	}
}

func TestReopenKeepsRows(t *testing.T) {
	path := filepath.Join(privateDir(t), "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.UpsertMessages(ctx, []Message{sample("r1", testAt())}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.GetMessage(ctx, "r1")
	if err != nil || got.ID != "r1" {
		t.Fatalf("reopen lost row: %v %+v", err, got)
	}
	if err := privfile.Check(path); err != nil {
		t.Fatalf("db not private after reopen: %v", err)
	}
}

func TestOpenSpillDirPrivateBeforeLock(t *testing.T) {
	path := filepath.Join(privateDir(t), "mail.duckdb")
	db, err := openWith(path, Options{}, func(ex execer) execer {
		ctx := context.Background()
		for _, q := range []string{
			"SET memory_limit = '32MB'",
			"SET threads = 1",
			"SET preserve_insertion_order = false",
		} {
			if _, err := ex.ExecContext(ctx, q); err != nil {
				return errExec{err}
			}
		}
		return ex
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	spill := path + ".tmp"
	assertDirPrivate(t, spill)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	seen := false
	errc := make(chan error, 1)
	go func() {
		_, err := db.QuerySQL(ctx, `SELECT count(*) FROM (SELECT * FROM (SELECT repeat('x', 64) || i::VARCHAR AS s FROM range(300000) t(i)) ORDER BY s)`, false)
		errc <- err
	}()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !seen {
		select {
		case err := <-errc:
			if err != nil {
				t.Fatal(err)
			}
			if !seen {
				t.Fatal("spill query finished without private spill files")
			}
			return
		case <-ctx.Done():
			t.Fatal("spill query timed out")
		case <-ticker.C:
			entries, err := os.ReadDir(spill)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				t.Fatal(err)
			}
			for _, e := range entries {
				p := filepath.Join(spill, e.Name())
				fi, err := os.Lstat(p)
				if err != nil {
					if os.IsNotExist(err) {
						continue
					}
					t.Fatal(err)
				}
				if fi.Mode()&os.ModeSymlink != 0 {
					t.Fatal("followed or created a spill symlink")
				}
				if fi.IsDir() {
					assertDirPrivate(t, p)
					continue
				}
				if err := privfile.Check(p); err != nil {
					t.Fatalf("spill file not private while open: %v", err)
				}
				seen = true
			}
		}
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

type errExec struct{ err error }

func (e errExec) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, e.err
}

func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "db")
	if err := privfile.MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testAt() time.Time {
	return time.Date(2024, 1, 2, 15, 4, 5, 0, time.UTC)
}

func TestOpenRecoversRowsAfterUncleanExit(t *testing.T) {
	if os.Getenv("STORE_WAL_CHILD") == "1" {
		path := os.Getenv("STORE_DB_PATH")
		db, err := Open(path)
		if err != nil {
			os.Exit(2)
		}
		if err := db.UpsertMessages(context.Background(), []Message{sample("u1", testAt())}); err != nil {
			_ = db.Close()
			os.Exit(3)
		}
		if err := os.WriteFile(path+".ready", []byte("1"), 0o600); err != nil {
			_ = db.Close()
			os.Exit(4)
		}
		select {}
	}
	path := filepath.Join(dbParent(t), "mail.duckdb")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), "STORE_WAL_CHILD=1", "STORE_DB_PATH="+path)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := path + ".ready"
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Lstat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("child did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	wal := path + ".wal"
	if _, err := os.Lstat(wal); err != nil {
		_ = cmd.Process.Kill()
		t.Fatal("expected wal while the child holds the database open")
	}
	if err := privfile.Check(wal); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("wal not private while child is open: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.GetMessage(context.Background(), "u1")
	if err != nil || got.ID != "u1" {
		t.Fatalf("unclean exit lost row: %v %+v", err, got)
	}
}
