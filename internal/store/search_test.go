package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/search"
)

func searchHits(t *testing.T, db *DB, q string) []Message {
	t.Helper()
	hits, err := db.ListMessages(context.Background(), ListFilter{Limit: 10, Query: q})
	if err != nil {
		t.Fatalf("%q: %v", q, err)
	}
	return hits
}

func searchIDs(t *testing.T, db *DB, q string) []string {
	t.Helper()
	return ids(searchHits(t, db, q))
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	slices.Sort(out)
	return out
}

func seedSearchMsgs(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	andSplit := sample("and-split", t1)
	andSplit.Subject = "monthly invoice"
	andSplit.Body = "leftover notes"
	andSplit.HasBody = true

	andPhrase := sample("and-phrase", t1.Add(time.Hour))
	andPhrase.Subject = "invoice leftover"

	pine := sample("pine-apple", t1.Add(2*time.Hour))
	pine.Subject = "pineapple tart"
	pine.Snippet = "the fruit"

	cone := sample("pine-cone", t1.Add(3*time.Hour))
	cone.Subject = "pine cone"
	cone.Body = "running late"
	cone.HasBody = true

	bob := sample("bob-smith", t1.Add(4*time.Hour))
	bob.FromName = "Bob Smith"
	bob.FromEmail = "bob@x.com"
	bob.Subject = "payment received"
	bob.Body = `100% foo_bar path\win`
	bob.HasBody = true

	if err := db.UpsertMessages(ctx, []Message{andSplit, andPhrase, pine, cone, bob}); err != nil {
		t.Fatal(err)
	}
}

func TestSearchANDTermsNotWholeSubstring(t *testing.T) {
	db := testDB(t)
	seedSearchMsgs(t, db)

	got := sortedCopy(searchIDs(t, db, "invoice leftover"))
	if !slices.Equal(got, []string{"and-phrase", "and-split"}) {
		t.Fatalf("AND terms: %#v", got)
	}
	got = searchIDs(t, db, `"invoice leftover"`)
	if !slices.Equal(got, []string{"and-phrase"}) {
		t.Fatalf("quoted phrase: %#v", got)
	}
}

func TestSearchQuotedFilterValues(t *testing.T) {
	db := testDB(t)
	seedSearchMsgs(t, db)

	got := searchIDs(t, db, `from:"Bob Smith"`)
	if !slices.Equal(got, []string{"bob-smith"}) {
		t.Fatalf("from quoted: %#v", got)
	}
	got = searchIDs(t, db, `subject:"payment received"`)
	if !slices.Equal(got, []string{"bob-smith"}) {
		t.Fatalf("subject quoted: %#v", got)
	}
	got = searchIDs(t, db, `"from:alice"`)
	if len(got) != 0 {
		t.Fatalf("quoted operator literal: %#v", got)
	}
}

func TestSearchLiteralPercentUnderscoreBackslash(t *testing.T) {
	db := testDB(t)
	seedSearchMsgs(t, db)

	if got := searchIDs(t, db, `100%`); !slices.Equal(got, []string{"bob-smith"}) {
		t.Fatalf("percent: %#v", got)
	}
	if got := searchIDs(t, db, `foo_bar`); !slices.Equal(got, []string{"bob-smith"}) {
		t.Fatalf("underscore: %#v", got)
	}
	if got := searchIDs(t, db, `path\win`); !slices.Equal(got, []string{"bob-smith"}) {
		t.Fatalf("backslash: %#v", got)
	}
	if got := searchIDs(t, db, `fooXbar`); len(got) != 0 {
		t.Fatalf("underscore must be literal: %#v", got)
	}
	if got := searchIDs(t, db, `from:%`); len(got) != 0 {
		t.Fatalf("from percent: %#v", got)
	}
	if got := searchIDs(t, db, `"path\win"`); !slices.Equal(got, []string{"bob-smith"}) {
		t.Fatalf("quoted backslash membership: %#v", got)
	}
}

func TestSearchValidationErrors(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	queries := []string{
		"after:nope",
		"before:2024-13-40",
		"from:a from:b",
		"unread is:unread",
		"label:inbox",
		`"foo`,
		`""`,
		strings.Repeat("你", search.MaxQueryRunes+1),
		"after:2024-06-01 before:2024-01-01",
	}
	for _, q := range queries {
		_, err := db.ListMessages(ctx, ListFilter{Limit: 10, Query: q})
		if !search.IsValidation(err) {
			t.Fatalf("%q: want validation, got %v", q, err)
		}
	}
}

func TestSearchFTSMatchesLiteralSet(t *testing.T) {
	db := testDB(t)
	seedSearchMsgs(t, db)
	ctx := context.Background()

	queries := []string{
		"invoice leftover",
		`"invoice leftover"`,
		"pine",
		"the",
		"runnin",
		"payment received",
		`"payment received"`,
		"the pine",
	}
	before := map[string][]string{}
	for _, q := range queries {
		before[q] = sortedCopy(searchIDs(t, db, q))
		if len(before[q]) == 0 {
			t.Fatalf("%q: expected literal matches", q)
		}
	}

	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range queries {
		got := sortedCopy(searchIDs(t, db, q))
		if !slices.Equal(got, before[q]) {
			t.Fatalf("%q: fts %#v want %#v", q, got, before[q])
		}
	}
	if !slices.Equal(before["pine"], []string{"pine-apple", "pine-cone"}) {
		t.Fatalf("pine set %#v", before["pine"])
	}
	if !slices.Equal(before["the"], []string{"pine-apple"}) {
		t.Fatalf("stopword set %#v", before["the"])
	}
	if !slices.Equal(before["runnin"], []string{"pine-cone"}) {
		t.Fatalf("nonstem set %#v", before["runnin"])
	}
}

func TestSearchSQLWriteStillMatches(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	msg := sample("m1", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	msg.Subject = "originalxyz"
	if err := db.UpsertMessages(ctx, []Message{msg}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QuerySQL(ctx, "UPDATE messages SET subject = 'rewrittenxyz' WHERE id = 'm1'", true); err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(t, db, "rewrittenxyz"); !slices.Equal(got, []string{"m1"}) {
		t.Fatalf("live column miss: %#v", got)
	}
	if got := searchIDs(t, db, "originalxyz"); len(got) != 0 {
		t.Fatalf("stale cache hit: %#v", got)
	}
}

func TestSearchTieOrderAndPagination(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	a := sample("a", at)
	a.Subject = "tie pineapple"
	b := sample("b", at)
	b.Subject = "tie pineapple"
	c := sample("c", at.Add(time.Hour))
	c.Subject = "tie pineapple"
	if err := db.UpsertMessages(ctx, []Message{a, b, c}); err != nil {
		t.Fatal(err)
	}

	got := searchIDs(t, db, "pineapple")
	if !slices.Equal(got, []string{"c", "b", "a"}) {
		t.Fatalf("no fts order: %#v", got)
	}
	page, err := db.ListMessages(ctx, ListFilter{Limit: 1, Query: "pineapple"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids(page), []string{"c"}) {
		t.Fatalf("page %#v", ids(page))
	}
	next, err := db.ListMessages(ctx, ListFilter{Limit: 1, Offset: 1, Query: "pineapple"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids(next), []string{"b"}) {
		t.Fatalf("offset %#v", ids(next))
	}

	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	got = searchIDs(t, db, "pineapple")
	if !slices.Equal(got, []string{"c", "b", "a"}) {
		t.Fatalf("fts tie order: %#v", got)
	}
}

func TestSearchRankUsesFTSWhenReady(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	weak := sample("weak", at)
	weak.Subject = "pineapple note"
	strong := sample("strong", at.Add(time.Hour))
	strong.Subject = "pineapple pineapple pineapple"
	if err := db.UpsertMessages(ctx, []Message{weak, strong}); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFTS(ctx); err != nil {
		t.Fatal(err)
	}
	got := searchIDs(t, db, "pineapple")
	if !slices.Equal(got, []string{"strong", "weak"}) {
		t.Fatalf("rank %#v", got)
	}
}
