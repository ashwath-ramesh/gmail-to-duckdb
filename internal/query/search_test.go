package query

import (
	"context"
	"strings"
	"testing"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/search"
)

func TestSearchRejectsInvalidQuery(t *testing.T) {
	_, err := Search(context.Background(), testDB(t), "after:nope")
	if !search.IsValidation(err) {
		t.Fatalf("got %v", err)
	}
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "after") {
		t.Fatalf("msg %v", err)
	}
}
