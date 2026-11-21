package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

const (
	SchemaVersion      = 4
	StateSchemaVersion = "schema_version"
	StateLastSyncOK    = "last_sync_ok"
	StateLastSyncError = "last_sync_error"
	StateProfileEmail  = "profile_email"

	// Sync cursor keys. Only CommitSyncPage and migrations write them.
	StateHistoryID    = "history_id"
	StateListPage     = "list_page_token"
	StateHistoryPage  = "history_page_token"
	StateHistoryStart = "history_start_id"
	StateFullStart    = "full_start_history_id"
	StateFullPhase    = "full_phase"

	// Full-sync phases stored in StateFullPhase.
	PhaseList     = "list"
	PhaseListFull = "list_full"
	PhaseCatchup  = "catchup"
)

const schema = `
CREATE TABLE IF NOT EXISTS messages (
  id VARCHAR PRIMARY KEY,
  thread_id VARCHAR NOT NULL,
  history_id UBIGINT,
  internal_date TIMESTAMP NOT NULL,
  from_name VARCHAR,
  from_email VARCHAR,
  to_emails VARCHAR[],
  cc_emails VARCHAR[],
  subject VARCHAR,
  snippet VARCHAR,
  body VARCHAR,
  size_bytes INTEGER,
  label_ids VARCHAR[],
  is_read BOOLEAN,
  is_outgoing BOOLEAN,
  is_deleted BOOLEAN DEFAULT false,
  has_body BOOLEAN DEFAULT false,
  body_fetched BOOLEAN DEFAULT false,
  synced_at TIMESTAMP,
  search_text VARCHAR,
  search_revision UBIGINT DEFAULT 0,
  headers JSON
);
CREATE TABLE IF NOT EXISTS labels (
  id VARCHAR PRIMARY KEY,
  name VARCHAR,
  type VARCHAR
);
CREATE TABLE IF NOT EXISTS sync_state (
  key VARCHAR PRIMARY KEY,
  value VARCHAR
);
CREATE TABLE IF NOT EXISTS sync_seen (
  id VARCHAR PRIMARY KEY
);
`

const schemaIndexes = `
CREATE INDEX IF NOT EXISTS messages_internal_date ON messages(internal_date);
CREATE INDEX IF NOT EXISTS messages_from_email ON messages(from_email);
CREATE INDEX IF NOT EXISTS messages_thread_id ON messages(thread_id);
CREATE INDEX IF NOT EXISTS messages_is_deleted ON messages(is_deleted);
`

func unsupportedSchema(ver int) error {
	if ver < 0 || ver > SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", ver)
	}
	return nil
}

func (d *DB) rejectUnsupportedSchema(ctx context.Context) error {
	ver, err := d.schemaVersion(ctx)
	if err != nil {
		return err
	}
	return unsupportedSchema(ver)
}

func (d *DB) migrateSchema(ctx context.Context) error {
	ver, err := d.schemaVersion(ctx)
	if err != nil {
		return err
	}
	if err := unsupportedSchema(ver); err != nil {
		return err
	}
	if ver == SchemaVersion {
		return nil
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if ver < 2 {
		if err := migrateToV2Schema(ctx, tx); err != nil {
			return err
		}
	}
	if ver < 3 {
		if err := migrateToV3Schema(ctx, tx); err != nil {
			return err
		}
	}
	if ver < 4 {
		if err := migrateToV4Schema(ctx, tx); err != nil {
			return err
		}
	}
	if ver < 2 {
		if err := migrateToV2Data(ctx, tx); err != nil {
			return err
		}
	}
	if ver < 3 {
		if err := migrateToV3Data(ctx, tx); err != nil {
			return err
		}
	}
	if ver < 4 {
		if err := migrateToV4Data(ctx, tx); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO sync_state(key, value) VALUES (?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value
`, StateSchemaVersion, strconv.Itoa(SchemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

func migrateToV2Schema(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN IF NOT EXISTS body_fetched BOOLEAN DEFAULT false`); err != nil {
		return fmt.Errorf("body_fetched: %w", err)
	}
	return nil
}

func migrateToV2Data(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET body_fetched = true WHERE has_body`); err != nil {
		return fmt.Errorf("body_fetched backfill: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET has_body = (body IS NOT NULL AND body <> '')`); err != nil {
		return fmt.Errorf("has_body normalize: %w", err)
	}
	return nil
}

func migrateToV3Schema(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN IF NOT EXISTS headers JSON`); err != nil {
		return fmt.Errorf("headers: %w", err)
	}
	return nil
}

func migrateToV3Data(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE messages SET is_outgoing = COALESCE(list_contains(COALESCE(label_ids, []::VARCHAR[]), 'SENT'), false)
`); err != nil {
		return fmt.Errorf("is_outgoing backfill: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE messages SET body_fetched = false
WHERE COALESCE(body_fetched, false) AND (body IS NULL OR body = '')
`); err != nil {
		return fmt.Errorf("requeue empty bodies: %w", err)
	}
	if err := discardUnsafeCursorsTx(ctx, tx); err != nil {
		return err
	}
	return markLegacySearchRepair(ctx, tx)
}

func migrateToV4Schema(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN IF NOT EXISTS search_revision UBIGINT DEFAULT 0`); err != nil {
		return fmt.Errorf("search_revision: %w", err)
	}
	return nil
}

func migrateToV4Data(ctx context.Context, tx *sql.Tx) error {
	if err := initSearchMetaTx(ctx, tx); err != nil {
		return err
	}
	return markLegacySearchRepair(ctx, tx)
}

func discardUnsafeCursorsTx(ctx context.Context, tx *sql.Tx) error {
	phase, hasPhase, err := queryState(ctx, tx, StateFullPhase)
	if err != nil {
		return err
	}
	anchor, hasAnchor, err := queryState(ctx, tx, StateFullStart)
	if err != nil {
		return err
	}
	if hasPhase && KnownFullPhase(phase) && hasAnchor && ParseStateUint(anchor) > 0 {
		return nil
	}
	_, hasList, err := queryState(ctx, tx, StateListPage)
	if err != nil {
		return err
	}
	for _, key := range []string{StateListPage, StateHistoryPage, StateHistoryStart} {
		if err := clearStateTx(ctx, tx, key); err != nil {
			return err
		}
	}
	if !hasPhase && !hasList {
		return nil
	}
	if err := setStateTx(ctx, tx, StateFullPhase, PhaseListFull); err != nil {
		return err
	}
	return clearStateTx(ctx, tx, StateFullStart)
}

// KnownFullPhase reports whether phase is a full-sync phase that this version writes.
func KnownFullPhase(phase string) bool {
	return phase == PhaseList || phase == PhaseListFull || phase == PhaseCatchup
}

// ParseStateUint parses a numeric state value. A bad value gives 0.
func ParseStateUint(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}

func (d *DB) schemaVersion(ctx context.Context) (int, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `
SELECT count(*) FROM information_schema.tables
WHERE table_schema = 'main' AND table_name = 'sync_state'
`).Scan(&n)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	v, ok, err := d.GetState(ctx, StateSchemaVersion)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	ver, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("schema_version: %w", err)
	}
	return ver, nil
}
