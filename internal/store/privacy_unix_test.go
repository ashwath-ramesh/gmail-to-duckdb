//go:build unix

package store

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
)

func makePermissiveFile(path string) error {
	return os.Chmod(path, 0o644)
}

func makePermissiveDir(path string) error {
	return os.Chmod(path, 0o755)
}

func dbParent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOpenAcceptsTraversableParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "custom")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	before := snapshotDir(t, parent)
	path := filepath.Join(parent, "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if snapshotDir(t, parent) != before {
		t.Fatal("custom parent changed")
	}
	if err := privfile.Check(path); err != nil {
		t.Fatalf("db not private while open: %v", err)
	}
}

func TestOpenRejectsWritableParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "custom")
	if err := os.Mkdir(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	before := snapshotDir(t, parent)
	path := filepath.Join(parent, "mail.duckdb")
	db, err := Open(path)
	if err == nil {
		_ = db.Close()
		t.Fatal("expected writable parent reject")
	}
	if db != nil {
		t.Fatal("db on failure")
	}
	if !strings.Contains(err.Error(), "writable") {
		t.Fatalf("want writable parent error, got %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("must not create db in writable parent")
	}
	if snapshotDir(t, parent) != before {
		t.Fatal("custom parent changed")
	}
}

func assertDirPrivate(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatalf("not a directory: %s", info.Mode())
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %o", info.Mode().Perm())
	}
}

func snapshotDir(t *testing.T, dir string) string {
	t.Helper()
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm().String()
}

func TestOpenFreshDBPrivateUnderPermissiveUmask(t *testing.T) {
	for _, mask := range []int{0o000, 0o002} {
		t.Run(strconv.FormatInt(int64(mask), 8), func(t *testing.T) {
			runUmaskChild(t, mask)
		})
	}
}

func runUmaskChild(t *testing.T, mask int) {
	t.Helper()
	if os.Getenv("STORE_PRIVACY_CHILD") == "umask" {
		want, err := strconv.ParseInt(os.Getenv("STORE_UMASK"), 8, 0)
		if err != nil {
			os.Exit(2)
		}
		syscall.Umask(int(want))
		path := os.Getenv("STORE_DB_PATH")
		db, err := Open(path)
		if err != nil {
			os.Exit(3)
		}
		if err := privfile.Check(path); err != nil {
			_ = db.Close()
			os.Exit(4)
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			_ = db.Close()
			os.Exit(5)
		}
		if err := db.Close(); err != nil {
			os.Exit(6)
		}
		os.Exit(0)
	}
	if os.Getenv("STORE_PRIVACY_CHILD") != "" {
		return
	}
	dir := privateDir(t)
	path := filepath.Join(dir, "mail.duckdb")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		"STORE_PRIVACY_CHILD=umask",
		"STORE_UMASK="+strconv.FormatInt(int64(mask), 8),
		"STORE_DB_PATH="+path,
	)
	out, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("umask child timed out: %s", out)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		t.Fatalf("umask %o child exit %d: %s", mask, ee.ExitCode(), out)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestWALPrivateWhileOpenAndAfterCheckpoint(t *testing.T) {
	path := filepath.Join(privateDir(t), "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.UpsertMessages(ctx, []Message{sample("c1", testAt())}); err != nil {
		t.Fatal(err)
	}
	wal := path + ".wal"
	if _, err := os.Lstat(wal); err != nil {
		t.Fatal("expected wal while the database is open after a commit")
	}
	if err := privfile.Check(wal); err != nil {
		t.Fatalf("wal not private while open: %v", err)
	}
	if _, err := db.sql.ExecContext(ctx, "CHECKPOINT"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessages(ctx, []Message{sample("c2", testAt().Add(time.Hour))}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(wal); err != nil {
		t.Fatal("wal missing after checkpoint rewrite")
	}
	if err := privfile.Check(wal); err != nil {
		t.Fatalf("recreated wal not private while open: %v", err)
	}
}

func TestOpenRejectsForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("need root to chown")
	}
	dir := privateDir(t)
	path := filepath.Join(dir, "mail.duckdb")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err == nil {
		_ = db.Close()
		t.Fatal("expected owner reject")
	}
	if db != nil {
		t.Fatal("db on failure")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep" {
		t.Fatal("unowned file changed")
	}
}
