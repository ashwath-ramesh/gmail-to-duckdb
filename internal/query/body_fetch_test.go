package query

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

func TestGetJSONBodyFetched(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []store.Message{
		{ID: "pending", ThreadID: "t", InternalDate: at, FromEmail: "a@x.com", Subject: "P"},
		{ID: "full", ThreadID: "t", InternalDate: at, FromEmail: "a@x.com", Subject: "F", Body: "hello", HasBody: true, BodyFetched: true},
		{ID: "empty", ThreadID: "t", InternalDate: at, FromEmail: "a@x.com", Subject: "E"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateBody(ctx, "empty", ""); err != nil {
		t.Fatal(err)
	}

	pending, err := Get(ctx, db, "pending", true)
	if err != nil {
		t.Fatal(err)
	}
	assertBodyFlags(t, pending.Message, false, false)

	empty, err := Get(ctx, db, "empty", true)
	if err != nil {
		t.Fatal(err)
	}
	assertBodyFlags(t, empty.Message, false, true)
	if empty.Message.Body != "" {
		t.Fatalf("empty body %q", empty.Message.Body)
	}

	full, err := Get(ctx, db, "full", true)
	if err != nil {
		t.Fatal(err)
	}
	assertBodyFlags(t, full.Message, true, true)
	if full.Message.Body != "hello" {
		t.Fatalf("body %q", full.Message.Body)
	}

	st, err := Status(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if st.BodyCoverage.WithBody != 1 || st.BodyCoverage.Total != 3 {
		t.Fatalf("coverage %+v", st.BodyCoverage)
	}
}

func assertBodyFlags(t *testing.T, m *Message, hasBody, fetched bool) {
	t.Helper()
	if m == nil {
		t.Fatal("missing message")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	if _, ok := obj["body_fetched"]; !ok {
		t.Fatalf("json missing body_fetched: %s", raw)
	}
	if m.HasBody != hasBody || m.BodyFetched != fetched {
		t.Fatalf("flags has_body=%v body_fetched=%v want %v %v json=%s", m.HasBody, m.BodyFetched, hasBody, fetched, raw)
	}
	if hb, ok := obj["has_body"].(bool); !ok || hb != hasBody {
		t.Fatalf("json has_body %#v", obj["has_body"])
	}
	if bf, ok := obj["body_fetched"].(bool); !ok || bf != fetched {
		t.Fatalf("json body_fetched %#v", obj["body_fetched"])
	}
}
