//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/web"
)

func emptyDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "mail.duckdb")
	db, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func waitServeFile(t *testing.T, dbPath string) web.ServeInfo {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if info, err := web.ReadServeFile(dbPath); err == nil {
			return info
		}
		if time.Now().After(deadline) {
			t.Fatal("serve file not written")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestServeSignalsCleanUp(t *testing.T) {
	bin := serveBenchBin(t)
	stub := t.TempDir()
	writeOpenStub(t, stub)
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		dbPath := emptyDB(t)
		cmd, stdout := startServe(t, bin, stub, dbPath)
		waitListening(t, stdout, 15*time.Second)
		waitServeFile(t, dbPath)
		if err := cmd.Process.Signal(sig); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatalf("%v: serve did not stop", sig)
		}
		for _, p := range []string{web.ServePath(dbPath), web.LauncherPath(dbPath)} {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatalf("%v: %s left behind: %v", sig, p, err)
			}
		}
	}
}

func TestCLIAfterServeKilledOrFrozen(t *testing.T) {
	bin := serveBenchBin(t)
	stub := t.TempDir()
	writeOpenStub(t, stub)
	dbPath := emptyDB(t)
	cmd, stdout := startServe(t, bin, stub, dbPath)
	waitListening(t, stdout, 15*time.Second)
	waitServeFile(t, dbPath)

	// A frozen serve still holds the DuckDB lock: report it, and keep its file.
	if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	// Signal returns before all threads of serve stop, and a running thread
	// can still answer. Wait until the kernel reports the stop.
	var ws syscall.WaitStatus
	if _, err := syscall.Wait4(cmd.Process.Pid, &ws, syscall.WUNTRACED, nil); err != nil || !ws.Stopped() {
		t.Fatalf("serve did not stop: %v (status %v)", err, ws)
	}
	var out bytes.Buffer
	err := run([]string{"gmail-to-duckdb", "status", "--db", dbPath}, strings.NewReader(""), &out, &out)
	if err == nil || !strings.Contains(err.Error(), "still holds the database") {
		t.Fatalf("frozen serve: err %v", err)
	}
	if _, err := os.Stat(web.ServePath(dbPath)); err != nil {
		t.Fatalf("serve file of a live serve removed: %v", err)
	}

	// A killed serve leaves its file. The next command removes it and works.
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	out.Reset()
	if err := run([]string{"gmail-to-duckdb", "status", "--db", dbPath}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatalf("after kill: %v %s", err, out.String())
	}
	if _, err := os.Stat(web.ServePath(dbPath)); !os.IsNotExist(err) {
		t.Fatalf("stale serve file kept: %v", err)
	}
}
