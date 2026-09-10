package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBindAccountEmptyDB(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	if err := db.BindAccount(ctx, "Me@Example.com"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := db.GetState(ctx, StateProfileEmail)
	if err != nil || !ok || got != "Me@Example.com" {
		t.Fatalf("bound %q %v %v", got, ok, err)
	}
	if err := db.BindAccount(ctx, "me@example.com"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = db.GetState(ctx, StateProfileEmail)
	if got != "Me@Example.com" {
		t.Fatalf("case fold must not rewrite: %q", got)
	}
}

func TestBindAccountEmptyProfile(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	if err := db.BindAccount(ctx, "  "); !errors.Is(err, ErrEmptyProfile) {
		t.Fatalf("err %v", err)
	}
	if _, ok, err := db.GetState(ctx, StateProfileEmail); err != nil || ok {
		t.Fatalf("must not write: %v %v", ok, err)
	}
}

func TestBindAccountMismatch(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	if err := db.BindAccount(ctx, "a@x.com"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessages(ctx, []Message{sample("m1", time.Unix(1, 0).UTC())}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, "history_id", "9"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindAccount(ctx, "b@x.com"); !errors.Is(err, ErrAccountMismatch) {
		t.Fatalf("err %v", err)
	}
	got, _ := db.GetMessage(ctx, "m1")
	if got.Subject == "" {
		t.Fatal("mail changed")
	}
	hid, ok, err := db.GetState(ctx, "history_id")
	if err != nil || !ok || hid != "9" {
		t.Fatalf("cursor %q %v %v", hid, ok, err)
	}
}

func TestBindAccountPopulatedWithoutOwner(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	if err := db.UpsertMessages(ctx, []Message{sample("m1", time.Unix(1, 0).UTC())}); err != nil {
		t.Fatal(err)
	}
	if err := db.BindAccount(ctx, "a@x.com"); !errors.Is(err, ErrUnboundDatabase) {
		t.Fatalf("err %v", err)
	}
	if _, ok, err := db.GetState(ctx, StateProfileEmail); err != nil || ok {
		t.Fatalf("must not bind: %v %v", ok, err)
	}
}

func TestCommitSyncPageAtomic(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessages(ctx, []Message{sample("gone", at)}); err != nil {
		t.Fatal(err)
	}
	err := db.CommitSyncPage(ctx, PageCommit{
		Messages:    []Message{sample("keep", at)},
		Tombstones:  []string{"gone"},
		Seen:        []string{"keep"},
		ListPage:    StateValue("p1"),
		FullPhase:   StateValue("list"),
		FullStartID: StateValue("50"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetMessage(ctx, "keep"); err != nil {
		t.Fatal(err)
	}
	gone, err := db.GetMessage(ctx, "gone")
	if err != nil || !gone.IsDeleted {
		t.Fatalf("tombstone %+v %v", gone, err)
	}
	tok, ok, _ := db.GetState(ctx, "list_page_token")
	if !ok || tok != "p1" {
		t.Fatalf("token %q", tok)
	}
	phase, ok, _ := db.GetState(ctx, "full_phase")
	if !ok || phase != "list" {
		t.Fatalf("phase %q", phase)
	}
}
