//go:build unix

package privfile

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func assertPrivate(t *testing.T, path string) {
	t.Helper()
	if err := Check(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("not a regular file: %s", info.Mode())
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
}

func makePermissive(path string) error {
	return os.Chmod(path, 0o644)
}

func assertStillPermissive(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("check changed mode to %o", info.Mode().Perm())
	}
}

func makeDirNotCreatable(dir string) error {
	return os.Chmod(dir, 0o555)
}

func restoreDirCreatable(dir string) error {
	return os.Chmod(dir, 0o700)
}

func snapshotParent(t *testing.T, dir string) string {
	t.Helper()
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm().String()
}

func assertParentUnchanged(t *testing.T, dir string, before string) {
	t.Helper()
	got := snapshotParent(t, dir)
	if got != before {
		t.Fatalf("parent mode %s -> %s", before, got)
	}
}

func TestReadRejectsFIFO(t *testing.T) {
	runFIFOHelper(t, "read")
}

func TestHardenRejectsFIFO(t *testing.T) {
	runFIFOHelper(t, "harden")
}

func runFIFOHelper(t *testing.T, op string) {
	t.Helper()
	if os.Getenv("PRIVFILE_FIFO_CHILD") == op {
		path := os.Getenv("PRIVFILE_FIFO_PATH")
		var err error
		switch op {
		case "read":
			_, err = Read(path)
		case "harden":
			err = Harden(path)
		default:
			os.Exit(3)
		}
		if err == nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if os.Getenv("PRIVFILE_FIFO_CHILD") != "" {
		return
	}
	path := filepath.Join(t.TempDir(), "secret.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), "PRIVFILE_FIFO_CHILD="+op, "PRIVFILE_FIFO_PATH="+path)
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("hung on FIFO")
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 2 {
		t.Fatal("accepted FIFO")
	}
	if err != nil {
		t.Fatal(err)
	}
}

func assertNewAppDirPrivate(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("not a directory")
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %o", info.Mode().Perm())
	}
}
