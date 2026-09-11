package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

var (
	ErrEmptyProfile    = errors.New("empty gmail profile email")
	ErrAccountMismatch = errors.New("gmail account does not match this database")
	ErrUnboundDatabase = errors.New("database has mail but no owner; use a separate database")
)

func (d *DB) BindAccount(ctx context.Context, email string) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return ErrEmptyProfile
	}
	return d.withTx(ctx, func(tx *sql.Tx) error {
		var stored string
		err := tx.QueryRowContext(ctx, "SELECT value FROM sync_state WHERE key = ?", StateProfileEmail).Scan(&stored)
		if err == nil {
			if !strings.EqualFold(stored, email) {
				return ErrAccountMismatch
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM messages").Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrUnboundDatabase
		}
		return setStateTx(ctx, tx, StateProfileEmail, email)
	})
}
