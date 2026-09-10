package stats

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		dir = filepath.Join(dir, "db")
		if err := privfile.MkdirPrivate(dir); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "mail.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestAllAreSelects(t *testing.T) {
	qs, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) < 11 {
		t.Fatalf("got %d stats", len(qs))
	}
	ctx := context.Background()
	db := testDB(t)
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

func TestNewReportsAreSelects(t *testing.T) {
	for _, id := range []string{"top_sender_domains", "top_recipient_domains", "monthly_top_senders"} {
		q, err := Get(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if !strings.HasPrefix(strings.ToUpper(q.SQL), "SELECT") {
			t.Fatalf("%s must start with SELECT: %q", id, q.SQL)
		}
		if strings.Contains(strings.ToLower(q.Name+" "+q.SQL), "saving") {
			t.Fatalf("%s claims storage savings", id)
		}
	}
}

func TestTopSenderDomains(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 6, 3, 12, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []store.Message{
		msg("in-a", at, "Alice@Example.COM", 100, false, false, "INBOX"),
		msg("in-b", at, "bob@example.com", 50, false, false, "INBOX"),
		msg("in-c", at, "carol@Other.com", 20, false, false, "INBOX"),
		msg("out", at, "dave@x.com", 999, true, false, "SENT"),
		msg("del", at, "eve@x.com", 888, false, true, "INBOX"),
		msg("empty", at, "", 10, false, false, "INBOX"),
		msg("space", at, "   ", 10, false, false, "INBOX"),
		msg("no-at", at, "not-an-email", 10, false, false, "INBOX"),
		msg("no-dom", at, "user@", 10, false, false, "INBOX"),
		msg("no-loc", at, "@nodomain", 10, false, false, "INBOX"),
		msg("multi", at, "a@b@c.com", 10, false, false, "INBOX"),
		msg("spam", at, "spam@spam.com", 10, false, false, "SPAM"),
		msg("trash", at, "trash@trash.com", 5, false, false, "TRASH"),
		msg("tab", at, "user\t@tab.com", 10, false, false, "INBOX"),
		msg("nl", at, "user\n@nl.com", 10, false, false, "INBOX"),
		msg("cr", at, "user\r@cr.com", 10, false, false, "INBOX"),
		msg("lead-tab", at, "\tuser@leadtab.com", 10, false, false, "INBOX"),
		msg("trail-nl", at, "user@trailnl.com\n", 10, false, false, "INBOX"),
		msg("ff", at, "user@ff.com\f", 10, false, false, "INBOX"),
		msg("vt", at, "user\v@vt.com", 10, false, false, "INBOX"),
	}); err != nil {
		t.Fatal(err)
	}
	q, err := Get("top_sender_domains")
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.ExecSQL(ctx, q.SQL)
	if err != nil {
		t.Fatal(err)
	}
	got := domainRows(t, res)
	assertNoWhitespaceDomains(t, got)
	want := []domainRow{
		{"example.com", 2, 150},
		{"other.com", 1, 20},
		{"spam.com", 1, 10},
		{"trash.com", 1, 5},
	}
	if len(got) != len(want) {
		t.Fatalf("rows %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d got %#v want %#v", i, got[i], want[i])
		}
	}
}

func TestTopRecipientDomains(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 6, 3, 12, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []store.Message{
		{
			ID: "dup-to-cc", ThreadID: "t", InternalDate: at, FromEmail: "me@x.com",
			ToEmails: []string{"a@X.com", "b@X.com"}, CcEmails: []string{"c@Y.com"},
			SizeBytes: 1000, LabelIDs: []string{"SENT"}, IsOutgoing: true,
		},
		{
			ID: "dup-same", ThreadID: "t", InternalDate: at, FromEmail: "me@x.com",
			ToEmails: []string{"a@X.com"}, CcEmails: []string{"a@x.com"},
			SizeBytes: 200, LabelIDs: []string{"SENT"}, IsOutgoing: true,
		},
		{
			ID: "bad-rcpt", ThreadID: "t", InternalDate: at, FromEmail: "me@x.com",
			ToEmails:  []string{"not-an-email", "user@", "@nodomain", "a@b@c.com", ""},
			SizeBytes: 50, LabelIDs: []string{"SENT"}, IsOutgoing: true,
		},
		{
			ID: "incoming", ThreadID: "t", InternalDate: at, FromEmail: "other@z.com",
			ToEmails: []string{"someone@z.com"}, SizeBytes: 400, LabelIDs: []string{"INBOX"},
		},
		{
			ID: "deleted", ThreadID: "t", InternalDate: at, FromEmail: "me@x.com",
			ToEmails: []string{"gone@z.com"}, SizeBytes: 300, LabelIDs: []string{"SENT"},
			IsOutgoing: true, IsDeleted: true,
		},
		{
			ID: "spam-out", ThreadID: "t", InternalDate: at, FromEmail: "me@x.com",
			ToEmails: []string{"z@spam.com"}, SizeBytes: 7, LabelIDs: []string{"SENT", "SPAM"},
			IsOutgoing: true,
		},
		{
			ID: "trash-out", ThreadID: "t", InternalDate: at, FromEmail: "me@x.com",
			ToEmails: []string{"z@trash.com"}, SizeBytes: 3, LabelIDs: []string{"SENT", "TRASH"},
			IsOutgoing: true,
		},
		{
			ID: "ws-rcpt", ThreadID: "t", InternalDate: at, FromEmail: "me@x.com",
			ToEmails:  []string{"user\t@tab.com", "user\n@nl.com", "\tuser@leadtab.com"},
			CcEmails:  []string{"user@trailnl.com\n", "user@ff.com\f", "user\v@vt.com", "user\r@cr.com"},
			SizeBytes: 11, LabelIDs: []string{"SENT"}, IsOutgoing: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	q, err := Get("top_recipient_domains")
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.ExecSQL(ctx, q.SQL)
	if err != nil {
		t.Fatal(err)
	}
	got := domainRows(t, res)
	assertNoWhitespaceDomains(t, got)
	want := []domainRow{
		{"x.com", 2, 1200},
		{"spam.com", 1, 7},
		{"trash.com", 1, 3},
		{"y.com", 1, 1000},
	}
	if len(got) != len(want) {
		t.Fatalf("rows %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d got %#v want %#v", i, got[i], want[i])
		}
	}
}

func TestMonthlyTopSenders(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	now := time.Now().UTC()
	cur := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	start := cur.AddDate(0, -11, 0)
	next := cur.AddDate(0, 1, 0)
	before := start.Add(-time.Second)
	atStart := start
	inPrev := cur.AddDate(0, -1, 10)
	inCur := cur.Add(12 * time.Hour)
	atNext := next

	var msgs []store.Message
	msgs = append(msgs,
		msg("before", before, "old@x.com", 1, false, false, "INBOX"),
		msg("next", atNext, "future@x.com", 1, false, false, "INBOX"),
		msg("out", inCur, "out@x.com", 1, true, false, "SENT"),
		msg("del", inCur, "del@x.com", 1, false, true, "INBOX"),
		msg("empty", inCur, "", 1, false, false, "INBOX"),
		msg("alice-a", atStart, "alice@x.com", 1, false, false, "INBOX"),
		msg("alice-b", inCur, "alice@x.com", 1, false, false, "SPAM"),
		msg("bob-a", inPrev, "bob@x.com", 1, false, false, "TRASH"),
		msg("bob-b", inPrev, "bob@x.com", 1, false, false, "TRASH"),
	)
	for i := 1; i <= 26; i++ {
		msgs = append(msgs, msg(fmt.Sprintf("tie-%02d", i), inCur, fmt.Sprintf("a%02d@x.com", i), 1, false, false, "INBOX"))
	}
	if err := db.UpsertMessages(ctx, msgs); err != nil {
		t.Fatal(err)
	}
	q, err := Get("monthly_top_senders")
	if err != nil {
		t.Fatal(err)
	}
	if q.SQL != "" && !hasColumns(q.SQL, "month", "from_email", "count") {
		t.Fatalf("columns: %s", q.SQL)
	}
	res, err := db.ExecSQL(ctx, q.SQL)
	if err != nil {
		t.Fatal(err)
	}
	got := monthRows(t, res)
	startMonth := start.Format("2006-01-02")
	prevMonth := time.Date(inPrev.Year(), inPrev.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	curMonth := cur.Format("2006-01-02")

	var emails []string
	seen := map[string]bool{}
	for _, r := range got {
		if r.email == "old@x.com" || r.email == "future@x.com" || r.email == "out@x.com" || r.email == "del@x.com" || r.email == "" {
			t.Fatalf("excluded sender present: %#v", r)
		}
		if r.email == "a24@x.com" || r.email == "a25@x.com" || r.email == "a26@x.com" {
			t.Fatalf("tie loser present: %#v", r)
		}
		if !seen[r.email] {
			seen[r.email] = true
			emails = append(emails, r.email)
		}
	}
	if len(seen) != 25 {
		t.Fatalf("top senders %d %#v", len(seen), emails)
	}
	if !seen["alice@x.com"] || !seen["bob@x.com"] || !seen["a01@x.com"] || !seen["a23@x.com"] {
		t.Fatalf("missing expected senders %#v", seen)
	}

	want := []monthRow{
		{curMonth, "a01@x.com", 1},
		{curMonth, "a02@x.com", 1},
		{curMonth, "a03@x.com", 1},
		{curMonth, "a04@x.com", 1},
		{curMonth, "a05@x.com", 1},
		{curMonth, "a06@x.com", 1},
		{curMonth, "a07@x.com", 1},
		{curMonth, "a08@x.com", 1},
		{curMonth, "a09@x.com", 1},
		{curMonth, "a10@x.com", 1},
		{curMonth, "a11@x.com", 1},
		{curMonth, "a12@x.com", 1},
		{curMonth, "a13@x.com", 1},
		{curMonth, "a14@x.com", 1},
		{curMonth, "a15@x.com", 1},
		{curMonth, "a16@x.com", 1},
		{curMonth, "a17@x.com", 1},
		{curMonth, "a18@x.com", 1},
		{curMonth, "a19@x.com", 1},
		{curMonth, "a20@x.com", 1},
		{curMonth, "a21@x.com", 1},
		{curMonth, "a22@x.com", 1},
		{curMonth, "a23@x.com", 1},
		{curMonth, "alice@x.com", 1},
		{prevMonth, "bob@x.com", 2},
		{startMonth, "alice@x.com", 1},
	}
	if len(got) != len(want) {
		t.Fatalf("rows %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d got %#v want %#v", i, got[i], want[i])
		}
	}
}

func TestReportsEmptyInput(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	for _, id := range []string{"top_sender_domains", "top_recipient_domains", "monthly_top_senders"} {
		q, err := Get(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		res, err := db.ExecSQL(ctx, q.SQL)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if len(res.Rows) != 0 {
			t.Fatalf("%s: got %d rows", id, len(res.Rows))
		}
	}
}

func TestKeepsExistingSizeRanking(t *testing.T) {
	q, err := Get("top_sender_size")
	if err != nil {
		t.Fatal(err)
	}
	if q.Name != "Top senders by size" {
		t.Fatalf("name %q", q.Name)
	}
}

func msg(id string, at time.Time, from string, bytes int, out, del bool, labels ...string) store.Message {
	return store.Message{
		ID:           id,
		ThreadID:     "t",
		InternalDate: at,
		FromEmail:    from,
		SizeBytes:    bytes,
		LabelIDs:     labels,
		IsOutgoing:   out,
		IsDeleted:    del,
	}
}

type domainRow struct {
	domain string
	n      int64
	bytes  int64
}

var wsDomain = regexp.MustCompile(`^.*[[:space:]].*$`)

func assertNoWhitespaceDomains(t *testing.T, rows []domainRow) {
	t.Helper()
	for _, r := range rows {
		if wsDomain.MatchString(r.domain) {
			t.Fatalf("whitespace address accepted: %#v", r)
		}
		switch r.domain {
		case "tab.com", "nl.com", "cr.com", "leadtab.com", "trailnl.com", "ff.com", "vt.com":
			t.Fatalf("whitespace address accepted: %#v", r)
		}
	}
}

func domainRows(t *testing.T, res store.SQLResult) []domainRow {
	t.Helper()
	if len(res.Columns) < 3 {
		t.Fatalf("columns %#v", res.Columns)
	}
	out := make([]domainRow, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) < 3 {
			t.Fatalf("row %#v", row)
		}
		out = append(out, domainRow{
			domain: fmt.Sprint(row[0]),
			n:      asInt(t, row[1]),
			bytes:  asInt(t, row[2]),
		})
	}
	return out
}

type monthRow struct {
	month string
	email string
	n     int64
}

func monthRows(t *testing.T, res store.SQLResult) []monthRow {
	t.Helper()
	if len(res.Columns) < 3 {
		t.Fatalf("columns %#v", res.Columns)
	}
	out := make([]monthRow, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) < 3 {
			t.Fatalf("row %#v", row)
		}
		out = append(out, monthRow{
			month: asDate(t, row[0]),
			email: fmt.Sprint(row[1]),
			n:     asInt(t, row[2]),
		})
	}
	return out
}

func asInt(t *testing.T, v any) int64 {
	t.Helper()
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		t.Fatalf("not int: %T %v", v, v)
		return 0
	}
}

func asDate(t *testing.T, v any) string {
	t.Helper()
	switch d := v.(type) {
	case time.Time:
		return d.UTC().Format("2006-01-02")
	case string:
		if len(d) >= 10 {
			return d[:10]
		}
		t.Fatalf("not date: %q", d)
		return ""
	default:
		s := fmt.Sprint(d)
		if len(s) >= 10 {
			return s[:10]
		}
		t.Fatalf("not date: %T %v", v, v)
		return ""
	}
}

func hasColumns(sql string, names ...string) bool {
	low := strings.ToLower(sql)
	for _, n := range names {
		if !strings.Contains(low, strings.ToLower(n)) {
			return false
		}
	}
	return true
}
