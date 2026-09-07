package search

import (
	"testing"
	"time"
)

func TestParseEmpty(t *testing.T) {
	q := Parse("")
	if q != (Query{}) {
		t.Fatalf("%+v", q)
	}
}

func TestParseTextAndOperators(t *testing.T) {
	q := Parse("invoice from:bob@x.com to:jane subject:pay unread after:2024-01-01 before:2024-06-01 leftover")
	if q.From != "bob@x.com" {
		t.Fatalf("from %q", q.From)
	}
	if q.To != "jane" {
		t.Fatalf("to %q", q.To)
	}
	if q.Subject != "pay" {
		t.Fatalf("subject %q", q.Subject)
	}
	if !q.Unread {
		t.Fatal("unread")
	}
	if !q.After.Equal(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("after %v", q.After)
	}
	if !q.Before.Equal(time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("before %v", q.Before)
	}
	if q.Text != "invoice leftover" {
		t.Fatalf("text %q", q.Text)
	}
}

func TestParseIsUnread(t *testing.T) {
	q := Parse("is:unread hello")
	if !q.Unread || q.Text != "hello" {
		t.Fatalf("%+v", q)
	}
}

func TestParseOperatorCase(t *testing.T) {
	q := Parse("FROM:Bob AFTER:2024-03-15")
	if q.From != "Bob" {
		t.Fatalf("from %q", q.From)
	}
	if !q.After.Equal(time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("after %v", q.After)
	}
}

func TestParseBadDateIgnored(t *testing.T) {
	q := Parse("after:nope before:2024-13-40 invoice from:")
	if !q.After.IsZero() || !q.Before.IsZero() {
		t.Fatalf("dates %+v", q)
	}
	if q.From != "" {
		t.Fatalf("empty from %q", q.From)
	}
	if q.Text != "invoice" {
		t.Fatalf("text %q", q.Text)
	}
}
