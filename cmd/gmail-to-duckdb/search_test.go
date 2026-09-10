package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSearchRejectsInvalidQuery(t *testing.T) {
	dbPath := seedDB(t)
	var out bytes.Buffer
	err := run([]string{"gmail-to-duckdb", "search", "--db", dbPath, "after:nope"}, strings.NewReader(""), &out, &out)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "after") {
		t.Fatalf("msg %v", err)
	}
}
