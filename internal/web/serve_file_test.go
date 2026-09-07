package web

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServeFileRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mail.duckdb")
	info := ServeInfo{URL: "http://127.0.0.1:8080", Token: "secret", PID: 12}
	if err := WriteServeFile(dbPath, info); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(ServePath(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
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
