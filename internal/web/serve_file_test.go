package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeFileRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mail.duckdb")
	info := ServeInfo{URL: "http://127.0.0.1:8080", Token: "secret", PID: 12}
	if err := WriteServeFile(dbPath, info); err != nil {
		t.Fatal(err)
	}
	assertPrivateFile(t, ServePath(dbPath))
	got, err := ReadServeFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != info {
		t.Fatalf("%+v", got)
	}
	if err := WriteServeFile(dbPath, ServeInfo{URL: "https://evil.example", Token: "x", PID: 1}); err == nil {
		t.Fatal("expected remote url reject")
	}
	if err := os.WriteFile(ServePath(dbPath), []byte(`{"url":"https://evil.example","token":"x","pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Dial(dbPath); err == nil {
		t.Fatal("expected Dial to reject remote url")
	}
	if err := RemoveServeFile(dbPath); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadServeFile(dbPath); err == nil {
		t.Fatal("expected missing file")
	}
}

func TestWriteServeFileReplacesPermissiveFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mail.duckdb")
	path := ServePath(dbPath)
	if err := os.WriteFile(path, []byte(`{"url":"http://127.0.0.1:1","token":"old","pid":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makePermissive(path); err != nil {
		t.Fatal(err)
	}
	info := ServeInfo{URL: "http://127.0.0.1:8080", Token: "new", PID: 2}
	if err := WriteServeFile(dbPath, info); err != nil {
		t.Fatal(err)
	}
	assertPrivateFile(t, path)
	got, err := ReadServeFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != info {
		t.Fatalf("%+v", got)
	}
}

func TestWriteServeFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mail.duckdb")
	target := filepath.Join(dir, "other.json")
	keep := []byte(`{"url":"http://127.0.0.1:1","token":"keep","pid":1}`)
	if err := os.WriteFile(target, keep, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, ServePath(dbPath)); err != nil {
		skipIfNoSymlink(t, err)
	}
	if err := WriteServeFile(dbPath, ServeInfo{URL: "http://127.0.0.1:8080", Token: "new", PID: 2}); err == nil {
		t.Fatal("expected symlink reject")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(keep) {
		t.Fatalf("target changed: %s", got)
	}
}

func TestReadServeFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mail.duckdb")
	target := filepath.Join(dir, "other.json")
	if err := os.WriteFile(target, []byte(`{"url":"http://127.0.0.1:8080","token":"x","pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, ServePath(dbPath)); err != nil {
		skipIfNoSymlink(t, err)
	}
	if _, err := ReadServeFile(dbPath); err == nil {
		t.Fatal("expected symlink reject")
	}
}

func TestFailedServeReplaceLeavesPriorData(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mail.duckdb")
	old := ServeInfo{URL: "http://127.0.0.1:8080", Token: "old", PID: 1}
	if err := WriteServeFile(dbPath, old); err != nil {
		t.Fatal(err)
	}
	if err := makeDirNotCreatable(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restoreDirCreatable(dir) })
	writeErr := WriteServeFile(dbPath, ServeInfo{URL: "http://127.0.0.1:9090", Token: "serve-secret-value", PID: 2})
	if writeErr == nil {
		t.Fatal("expected replace failure")
	}
	if err := restoreDirCreatable(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(ServePath(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	var got ServeInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("prior json lost: %v", err)
	}
	if got != old {
		t.Fatalf("prior data lost: %+v", got)
	}
	if strings.Contains(writeErr.Error(), "serve-secret-value") {
		t.Fatalf("error leaked secret: %v", writeErr)
	}
	assertNoTempFiles(t, dir)
}
