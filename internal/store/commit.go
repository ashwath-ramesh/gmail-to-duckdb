package store

import (
	"context"
	"database/sql"
)

// PageCommit is one metadata page: messages, tombstones, seen IDs, resume keys.
// A nil *string leaves that key unchanged. A pointer to "" clears it.
type PageCommit struct {
	Messages     []Message
	Tombstones   []string
	Seen         []string
	ResetSeen    bool
	MarkMissing  bool
	ListPage     *string
	HistoryPage  *string
	HistoryStart *string
	HistoryID    *string
	FullPhase    *string
	FullStartID  *string
}

func StateValue(v string) *string { return &v }

func StateClear() *string {
	s := ""
	return &s
}

func (d *DB) CommitSyncPage(ctx context.Context, p PageCommit) error {
	changed := len(p.Messages) > 0 || len(p.Tombstones) > 0 || p.MarkMissing
	err := d.withTx(ctx, func(tx *sql.Tx) error {
		if err := upsertMessagesTx(ctx, tx, p.Messages); err != nil {
			return err
		}
		if err := markDeletedTx(ctx, tx, p.Tombstones); err != nil {
			return err
		}
		if err := addSeenTx(ctx, tx, p.Seen); err != nil {
			return err
		}
		if p.MarkMissing {
			if _, err := markMissingDeletedTx(ctx, tx); err != nil {
				return err
			}
		} else if p.ResetSeen {
			if err := resetSeenTx(ctx, tx); err != nil {
				return err
			}
		}
		if err := applyStateTx(ctx, tx, "list_page_token", p.ListPage); err != nil {
			return err
		}
		if err := applyStateTx(ctx, tx, "history_page_token", p.HistoryPage); err != nil {
			return err
		}
		if err := applyStateTx(ctx, tx, "history_start_id", p.HistoryStart); err != nil {
			return err
		}
		if err := applyStateTx(ctx, tx, "history_id", p.HistoryID); err != nil {
			return err
		}
		if err := applyStateTx(ctx, tx, "full_phase", p.FullPhase); err != nil {
			return err
		}
		return applyStateTx(ctx, tx, "full_start_history_id", p.FullStartID)
	})
	if err == nil && changed {
		d.notifyIndex()
	}
	return err
}

func applyStateTx(ctx context.Context, tx *sql.Tx, key string, v *string) error {
	if v == nil {
		return nil
	}
	if *v == "" {
		return clearStateTx(ctx, tx, key)
	}
	return setStateTx(ctx, tx, key, *v)
}
