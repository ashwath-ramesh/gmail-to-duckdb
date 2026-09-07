package parse

import (
	"os"
	"testing"
	"time"
)

func TestMetadata(t *testing.T) {
	raw, err := os.ReadFile("testdata/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := Message(raw, "me@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if msg.ID != "18c1" || msg.ThreadID != "18c1t" {
		t.Fatalf("ids: %+v", msg)
	}
	if msg.HistoryID != 12345 {
		t.Fatalf("history: %d", msg.HistoryID)
	}
	if !msg.InternalDate.Equal(time.UnixMilli(1700000000000).UTC()) {
		t.Fatalf("date: %s", msg.InternalDate)
	}
	if msg.FromName != "Alice" || msg.FromEmail != "alice@example.com" {
		t.Fatalf("from: %s %s", msg.FromName, msg.FromEmail)
	}
	if len(msg.ToEmails) != 2 || msg.ToEmails[0] != "bob@example.com" {
		t.Fatalf("to: %#v", msg.ToEmails)
	}
	if len(msg.CcEmails) != 1 || msg.CcEmails[0] != "cc@example.com" {
		t.Fatalf("cc: %#v", msg.CcEmails)
	}
	if msg.Subject != "Hi" || msg.Snippet != "Hello there" {
		t.Fatalf("text: %+v", msg)
	}
	if msg.IsRead || msg.IsOutgoing || msg.HasBody || msg.IsDeleted {
		t.Fatalf("flags: %+v", msg)
	}
	if msg.SizeBytes != 4096 {
		t.Fatalf("size: %d", msg.SizeBytes)
	}
}

func TestFullBodyAndOutgoing(t *testing.T) {
	raw, err := os.ReadFile("testdata/full.json")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := Message(raw, "me@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !msg.HasBody || msg.Body != "Thanks for the note" {
		t.Fatalf("body: %q has=%v", msg.Body, msg.HasBody)
	}
	if !msg.IsOutgoing {
		t.Fatal("expected outgoing")
	}
	if !msg.IsRead {
		t.Fatal("sent should be read")
	}
}

func TestHeaderParse(t *testing.T) {
	name, email := ParseMailbox("Alice Smith <alice@Example.COM>")
	if name != "Alice Smith" || email != "alice@example.com" {
		t.Fatalf("%q %q", name, email)
	}
	emails := ParseAddressList("A <a@x.com>, b@y.com")
	if len(emails) != 2 || emails[0] != "a@x.com" || emails[1] != "b@y.com" {
		t.Fatalf("%#v", emails)
	}
}
