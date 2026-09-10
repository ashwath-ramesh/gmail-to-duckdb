package parse

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestMetadata(t *testing.T) {
	raw, err := os.ReadFile("testdata/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := Message(raw)
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
	msg, err := Message(raw)
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

func TestInvalidBase64IsError(t *testing.T) {
	raw := []byte(`{
		"id":"badb64","threadId":"t","labelIds":["INBOX"],
		"snippet":"x","historyId":"1","internalDate":"1700000000000",
		"payload":{"mimeType":"text/plain","headers":[{"name":"From","value":"a@x.com"}],
			"body":{"data":"!!!!not-valid-base64!!!!","size":8}}
	}`)
	msg, err := Message(raw)
	if err == nil {
		t.Fatalf("invalid base64 must not be swallowed: body=%q has_body=%v", msg.Body, msg.HasBody)
	}
}

func TestOutgoingUsesSENTNotFromMatch(t *testing.T) {
	alias := []byte(`{
		"id":"sent-alias","threadId":"t","labelIds":["SENT"],
		"snippet":"x","historyId":"1","internalDate":"1700000000000",
		"payload":{"headers":[{"name":"From","value":"alias@other.com"},{"name":"Subject","value":"Out"}]}
	}`)
	msg, err := Message(alias)
	if err != nil {
		t.Fatal(err)
	}
	if !msg.IsOutgoing {
		t.Fatalf("SENT alias must be outgoing: from=%q outgoing=%v", msg.FromEmail, msg.IsOutgoing)
	}

	selfIn := []byte(`{
		"id":"self-in","threadId":"t","labelIds":["INBOX"],
		"snippet":"x","historyId":"1","internalDate":"1700000000000",
		"payload":{"headers":[{"name":"From","value":"me@example.com"},{"name":"Subject","value":"In"}]}
	}`)
	got, err := Message(selfIn)
	if err != nil {
		t.Fatal(err)
	}
	if got.IsOutgoing {
		t.Fatalf("self incoming without SENT must not be outgoing: from=%q outgoing=%v", got.FromEmail, got.IsOutgoing)
	}
}

func TestHeadersPreserveDuplicatesAndOrder(t *testing.T) {
	raw := []byte(`{
		"id":"h1","threadId":"t","labelIds":["INBOX"],
		"snippet":"x","historyId":"1","internalDate":"1700000000000",
		"payload":{"headers":[
			{"name":"Received","value":"first"},
			{"name":"X-Custom","value":"a"},
			{"name":"received","value":"second"},
			{"name":"X-Custom","value":"b"}
		]}
	}`)
	msg, err := Message(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Headers) != 4 {
		t.Fatalf("headers %#v", msg.Headers)
	}
	if msg.Headers[0].Name != "Received" || msg.Headers[0].Value != "first" {
		t.Fatalf("first %+v", msg.Headers[0])
	}
	if msg.Headers[2].Name != "received" || msg.Headers[2].Value != "second" {
		t.Fatalf("dup case %+v", msg.Headers[2])
	}
}

func TestParsedEmptyHeadersAreEmptySlice(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"id":"e","threadId":"t","labelIds":["INBOX"],"payload":{"headers":[]}}`),
		[]byte(`{"id":"e","threadId":"t","labelIds":["INBOX"],"payload":{}}`),
	} {
		msg, err := Message(raw)
		if err != nil {
			t.Fatal(err)
		}
		if msg.Headers == nil || len(msg.Headers) != 0 {
			t.Fatalf("want [] not nil: %#v from %s", msg.Headers, raw)
		}
	}
}

func TestMissingReferencedTextIsError(t *testing.T) {
	raw := []byte(`{
		"id":"miss","threadId":"t","labelIds":["INBOX"],
		"payload":{"mimeType":"text/plain","body":{"size":20}}
	}`)
	if _, err := Message(raw); err == nil {
		t.Fatal("missing referenced text must stay pending")
	}
}

func TestHTMLFallbackAndAttachmentSkip(t *testing.T) {
	raw := []byte(`{
		"id":"html","threadId":"t","labelIds":["INBOX"],
		"payload":{"mimeType":"multipart/mixed","parts":[
			{"mimeType":"application/pdf","filename":"a.pdf","body":{"attachmentId":"att","size":12}},
			{"mimeType":"text/html","body":{"data":"PHA+SGk8L3A+"}}
		]}
	}`)
	msg, err := Message(raw)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Body != "<p>Hi</p>" || !msg.HasBody {
		t.Fatalf("html fallback: %+v", msg)
	}
}

func TestEmptyIDRejected(t *testing.T) {
	if _, err := Message([]byte(`{"threadId":"t"}`)); err == nil {
		t.Fatal("empty id must fail")
	}
}

func TestAttachedMessageTextIsNotMainBody(t *testing.T) {
	raw := []byte(`{
		"id":"mix","threadId":"t","labelIds":["INBOX"],
		"payload":{"mimeType":"multipart/mixed","parts":[
			{"mimeType":"text/html","body":{"data":"TUFJTkhUTUw="}},
			{"mimeType":"message/rfc822","filename":"fwd.eml","parts":[
				{"mimeType":"text/plain","body":{"data":"VU5JUVVFX0FUVEFDSF9NQVJLRVI="}}
			]}
		]}
	}`)
	msg, err := Message(raw)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Body != "MAINHTML" {
		t.Fatalf("main text: %q", msg.Body)
	}
	if strings.Contains(msg.Body, "UNIQUE_ATTACH_MARKER") {
		t.Fatal("attached text became body")
	}
}

func TestEmptyPlainFallsBackToHTML(t *testing.T) {
	raw := []byte(`{
		"id":"alt","threadId":"t","labelIds":["INBOX"],
		"payload":{"mimeType":"multipart/alternative","parts":[
			{"mimeType":"text/plain","body":{"data":"","size":0}},
			{"mimeType":"text/html","body":{"data":"PHA+SGk8L3A+"}}
		]}
	}`)
	msg, err := Message(raw)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Body != "<p>Hi</p>" || !msg.HasBody {
		t.Fatalf("html fallback %+v", msg)
	}
}

func TestLaterNonemptyPlainAfterEmptyPlain(t *testing.T) {
	raw := []byte(`{
		"id":"later","threadId":"t","labelIds":["INBOX"],
		"payload":{"mimeType":"multipart/mixed","parts":[
			{"mimeType":"text/plain","body":{"data":"","size":0}},
			{"mimeType":"text/plain","body":{"data":"TEFURVJwbGFpbg=="}}
		]}
	}`)
	msg, err := Message(raw)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Body != "LATERplain" || !msg.HasBody {
		t.Fatalf("later plain: %+v", msg)
	}
}

func TestCorruptBodyStillPending(t *testing.T) {
	raw := []byte(`{
		"id":"bad","threadId":"t","labelIds":["INBOX"],
		"payload":{"mimeType":"multipart/alternative","parts":[
			{"mimeType":"text/plain","body":{"data":"!!!!not-valid-base64!!!!","size":8}},
			{"mimeType":"text/html","body":{"data":"PHA+SGk8L3A+"}}
		]}
	}`)
	if _, err := Message(raw); err == nil {
		t.Fatal("corrupt plain must stay pending")
	}
}
