package search

import (
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func parseOK(t *testing.T, in string) Query {
	t.Helper()
	q, err := Parse(in)
	if err != nil {
		t.Fatalf("%q: %v", in, err)
	}
	return q
}

func parseErr(t *testing.T, in string) error {
	t.Helper()
	_, err := Parse(in)
	if err == nil {
		t.Fatalf("%q: want error", in)
	}
	if !IsValidation(err) {
		t.Fatalf("%q: not validation: %T %v", in, err, err)
	}
	return err
}

func TestParseEmpty(t *testing.T) {
	q := parseOK(t, "")
	if len(q.Terms) != 0 || q.From != "" || q.Unread {
		t.Fatalf("%+v", q)
	}
}

func TestParseTextAndOperators(t *testing.T) {
	q := parseOK(t, "invoice from:bob@x.com to:jane subject:pay unread after:2024-01-01 before:2024-06-01 leftover")
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
	if !slices.Equal(q.Terms, []string{"invoice", "leftover"}) {
		t.Fatalf("terms %#v", q.Terms)
	}
}

func TestParseIsUnread(t *testing.T) {
	q := parseOK(t, "is:unread hello")
	if !q.Unread || !slices.Equal(q.Terms, []string{"hello"}) {
		t.Fatalf("%+v", q)
	}
}

func TestParseOperatorCase(t *testing.T) {
	q := parseOK(t, "FROM:Bob AFTER:2024-03-15")
	if q.From != "Bob" {
		t.Fatalf("from %q", q.From)
	}
	if !q.After.Equal(time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("after %v", q.After)
	}
}

func TestParseRejectsBadDate(t *testing.T) {
	err := parseErr(t, "after:nope invoice")
	if !strings.Contains(strings.ToLower(err.Error()), "after") {
		t.Fatalf("msg %v", err)
	}
	err = parseErr(t, "before:2024-13-40")
	if !strings.Contains(strings.ToLower(err.Error()), "before") {
		t.Fatalf("msg %v", err)
	}
}

func TestParseRejectsEmptyAndRepeatedFilters(t *testing.T) {
	parseErr(t, "from:")
	parseErr(t, "to:")
	parseErr(t, "subject:")
	parseErr(t, "after:")
	parseErr(t, "before:")
	parseErr(t, "is:")
	parseErr(t, "from:a from:b")
	parseErr(t, "to:a TO:b")
	parseErr(t, "subject:a subject:b")
	parseErr(t, "after:2024-01-01 after:2024-02-01")
	parseErr(t, "before:2024-06-01 before:2024-07-01")
	parseErr(t, "unread unread")
	parseErr(t, "unread is:unread")
	parseErr(t, "is:unread unread")
}

func TestParseRejectsUnsupportedOperator(t *testing.T) {
	parseErr(t, "label:inbox")
	parseErr(t, "is:read")
	parseErr(t, "has:attachment")
	parseErr(t, "foo:bar")
}

func TestParseRejectsBeforeNotAfterAfter(t *testing.T) {
	parseErr(t, "after:2024-01-01 before:2024-01-01")
	parseErr(t, "after:2024-06-01 before:2024-01-01")
	q := parseOK(t, "after:2024-01-01 before:2024-01-02")
	if q.After.IsZero() || q.Before.IsZero() {
		t.Fatalf("%+v", q)
	}
}

func TestParseQuotedPhrasesAndFilters(t *testing.T) {
	q := parseOK(t, `"payment received" invoice`)
	if !slices.Equal(q.Terms, []string{"payment received", "invoice"}) {
		t.Fatalf("terms %#v", q.Terms)
	}

	q = parseOK(t, `from:"Bob Smith" subject:"payment received"`)
	if q.From != "Bob Smith" {
		t.Fatalf("from %q", q.From)
	}
	if q.Subject != "payment received" {
		t.Fatalf("subject %q", q.Subject)
	}
	if len(q.Terms) != 0 {
		t.Fatalf("terms %#v", q.Terms)
	}
}

func TestParseQuotedBypassesOperator(t *testing.T) {
	q := parseOK(t, `"from:alice"`)
	if q.From != "" || !slices.Equal(q.Terms, []string{"from:alice"}) {
		t.Fatalf("%+v", q)
	}
	q = parseOK(t, `"is:unread"`)
	if q.Unread || !slices.Equal(q.Terms, []string{"is:unread"}) {
		t.Fatalf("%+v", q)
	}
}

func TestParseEscapesInQuotes(t *testing.T) {
	q := parseOK(t, `"foo\"bar"`)
	if !slices.Equal(q.Terms, []string{`foo"bar`}) {
		t.Fatalf("terms %#v", q.Terms)
	}
	q = parseOK(t, `"foo\\bar"`)
	if !slices.Equal(q.Terms, []string{`foo\bar`}) {
		t.Fatalf("terms %#v", q.Terms)
	}
	q = parseOK(t, `subject:"a \"b\" c"`)
	if q.Subject != `a "b" c` {
		t.Fatalf("subject %q", q.Subject)
	}
}

func TestParseRejectsQuotes(t *testing.T) {
	parseErr(t, `"foo`)
	parseErr(t, `foo"`)
	parseErr(t, `""`)
	parseErr(t, `from:""`)
	parseErr(t, `from:"`)
	parseErr(t, `subject:"payment`)
}

func TestParseRejectsOver200Runes(t *testing.T) {
	ok := strings.Repeat("你", MaxQueryRunes)
	if utf8.RuneCountInString(ok) != MaxQueryRunes {
		t.Fatal("setup")
	}
	parseOK(t, ok)
	parseErr(t, ok+"你")
}

func TestParseLiteralSpecialsAreTerms(t *testing.T) {
	q := parseOK(t, `100% foo_bar path\win`)
	if !slices.Equal(q.Terms, []string{`100%`, `foo_bar`, `path\win`}) {
		t.Fatalf("terms %#v", q.Terms)
	}
}

func TestParseQuotedBackslashAndTrailingJunk(t *testing.T) {
	q := parseOK(t, `"path\win"`)
	if !slices.Equal(q.Terms, []string{`path\win`}) {
		t.Fatalf("quoted backslash %#v", q.Terms)
	}
	q = parseOK(t, `"foo\"bar"`)
	if !slices.Equal(q.Terms, []string{`foo"bar`}) {
		t.Fatalf("escaped quote %#v", q.Terms)
	}
	q = parseOK(t, `"foo\\bar"`)
	if !slices.Equal(q.Terms, []string{`foo\bar`}) {
		t.Fatalf("escaped slash %#v", q.Terms)
	}
	q = parseOK(t, `"from:alice"`)
	if q.From != "" || !slices.Equal(q.Terms, []string{"from:alice"}) {
		t.Fatalf("quoted colon %+v", q)
	}
	parseErr(t, `"hello"x`)
	parseErr(t, `subject:"hello"world`)
	parseErr(t, `"   "`)
	parseErr(t, `from:"   "`)
	parseErr(t, "label:inbox")
}
