package stats

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

func TestAllAreSelects(t *testing.T) {
	qs, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) < 8 {
		t.Fatalf("got %d stats", len(qs))
	}
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "mail.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.UpsertMessages(ctx, []store.Message{{
		ID: "m1", ThreadID: "t1", InternalDate: time.Date(2024, 6, 3, 0, 0, 0, 0, time.UTC),
		FromEmail: "a@x.com", Subject: "hi", LabelIDs: []string{"INBOX"}, SizeBytes: 100,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertLabels(ctx, []store.Label{{ID: "INBOX", Name: "Inbox"}}); err != nil {
		t.Fatal(err)
	}
	for _, q := range qs {
		if _, err := db.ExecSQL(ctx, q.SQL); err != nil {
			t.Fatalf("%s: %v", q.ID, err)
		}
	}
}

func TestGet(t *testing.T) {
	q, err := Get("top_senders")
	if err != nil {
		t.Fatal(err)
	}
	if q.Name != "Top senders" {
		t.Fatalf("name %q", q.Name)
	}
}
