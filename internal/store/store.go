package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/search"
	_ "github.com/duckdb/duckdb-go/v2"
)

const (
	maxLimit   = 100
	maxOffset  = 5000
	likeEscape = " ESCAPE '\\'"
	listSQL    = "CAST(? AS JSON)::VARCHAR[]"
)

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

const searchTextSQL = `trim(concat_ws(' ', from_name, from_email, array_to_string(to_emails, ' '), array_to_string(cc_emails, ' '), subject, snippet, body))`

const messageSelect = `
SELECT id, thread_id, history_id, internal_date,
       from_name, from_email, to_json(to_emails)::VARCHAR, to_json(cc_emails)::VARCHAR,
       subject, snippet, '', size_bytes, to_json(label_ids)::VARCHAR,
       is_read, is_outgoing, is_deleted, has_body, body_fetched, synced_at
`

type Message struct {
	ID           string
	ThreadID     string
	HistoryID    uint64
	InternalDate time.Time
	FromName     string
	FromEmail    string
	ToEmails     []string
	CcEmails     []string
	Subject      string
	Snippet      string
	Body         string
	SizeBytes    int
	LabelIDs     []string
	IsRead       bool
	IsOutgoing   bool
	IsDeleted    bool
	HasBody      bool
	BodyFetched  bool
	SyncedAt     time.Time
	Headers      []Header `json:"-"`
}

type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Label struct {
	ID   string
	Name string
	Type string
}

type ListFilter struct {
	Unread    bool
	Label     string
	From      string
	After     time.Time
	Before    time.Time
	Query     string
	AfterDate time.Time
	AfterID   string
	Limit     int
	Offset    int
}

type SQLResult struct {
	Columns     []string `json:"columns"`
	ColumnTypes []string `json:"column_types,omitempty"`
	Rows        [][]any  `json:"rows"`
}

type Coverage struct {
	WithBody int
	Total    int
}

func (c Coverage) SearchCovers() string {
	if c.Total == 0 || c.WithBody == 0 {
		return "metadata"
	}
	if c.WithBody == c.Total {
		return "bodies"
	}
	return "mixed"
}

type DB struct {
	sql *sql.DB
	mu  chan struct{}
}

func (d *DB) acquire(ctx context.Context) error {
	select {
	case d.mu <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *DB) release() {
	<-d.mu
}

type Options struct {
	DuckUI bool
}

func Open(path string) (*DB, error) {
	return OpenWith(path, Options{})
}

func OpenWith(path string, opt Options) (*DB, error) {
	return openWith(path, opt, nil)
}

func openWith(path string, opt Options, wrap func(execer) execer) (*DB, error) {
	path, spill, err := prepareOpen(path)
	if err != nil {
		return nil, err
	}
	sqldb, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
	sqldb.SetMaxOpenConns(1)
	if err := applyTempSettings(sqldb, spill); err != nil {
		_ = sqldb.Close()
		return nil, err
	}
	db := &DB{sql: sqldb, mu: make(chan struct{}, 1)}
	ctx := context.Background()
	if err := db.rejectUnsupportedSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := sqldb.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	if err := db.migrateSearch(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := db.migrateSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := sqldb.Exec(schemaIndexes); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("schema indexes: %w", err)
	}
	var ex execer = sqldb
	if wrap != nil {
		ex = wrap(sqldb)
	}
	if err := bootstrapTrusted(ex, opt); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := db.lockSQL(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func applyTempSettings(db *sql.DB, spill string) error {
	return execSet(db, "temp_directory", spill)
}

func execSet(db *sql.DB, name, value string) error {
	q := "SET " + name + " = '" + strings.ReplaceAll(value, "'", "''") + "'"
	if _, err := db.Exec(q); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func bootstrapTrusted(ex execer, opt Options) error {
	_ = loadExtension(ex, "fts")
	if opt.DuckUI {
		if err := loadExtension(ex, "ui"); err != nil {
			return fmt.Errorf("duckdb ui: %w", err)
		}
	}
	return nil
}

func loadExtension(ex execer, name string) error {
	ctx := context.Background()
	if _, err := ex.ExecContext(ctx, "LOAD "+name); err == nil {
		return nil
	}
	if _, err := ex.ExecContext(ctx, "INSTALL "+name); err != nil {
		return err
	}
	_, err := ex.ExecContext(ctx, "LOAD "+name)
	return err
}

func (d *DB) lockSQL() error {
	if _, err := d.sql.Exec("SET enable_external_access = false"); err != nil {
		return fmt.Errorf("enable_external_access: %w", err)
	}
	if _, err := d.sql.Exec("SET lock_configuration = true"); err != nil {
		return fmt.Errorf("lock_configuration: %w", err)
	}
	return nil
}

func (d *DB) Close() error {
	return d.sql.Close()
}

func (d *DB) SQL() *sql.DB {
	return d.sql
}

const upsertMessageSQL = `
INSERT INTO messages (
  id, thread_id, history_id, internal_date,
  from_name, from_email, to_emails, cc_emails,
  subject, snippet, body, size_bytes, label_ids,
  is_read, is_outgoing, is_deleted, has_body, body_fetched, synced_at, headers
) VALUES (
  ?, ?, ?, ?,
  ?, ?, ` + listSQL + `, ` + listSQL + `,
  ?, ?, ?, ?, ` + listSQL + `,
  ?, ?, ?, ?, ?, ?, CAST(? AS JSON)
)
ON CONFLICT (id) DO UPDATE SET
  thread_id = excluded.thread_id,
  history_id = excluded.history_id,
  internal_date = excluded.internal_date,
  from_name = excluded.from_name,
  from_email = excluded.from_email,
  to_emails = excluded.to_emails,
  cc_emails = excluded.cc_emails,
  subject = excluded.subject,
  snippet = excluded.snippet,
  body = CASE WHEN excluded.body_fetched THEN excluded.body ELSE messages.body END,
  size_bytes = excluded.size_bytes,
  label_ids = excluded.label_ids,
  is_read = excluded.is_read,
  is_outgoing = excluded.is_outgoing,
  is_deleted = excluded.is_deleted,
  has_body = CASE WHEN excluded.body_fetched THEN excluded.has_body ELSE messages.has_body END,
  body_fetched = messages.body_fetched OR excluded.body_fetched,
  synced_at = excluded.synced_at,
  headers = CASE WHEN excluded.headers IS NOT NULL THEN excluded.headers ELSE messages.headers END
`

func (d *DB) UpsertMessages(ctx context.Context, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}
	if err := d.acquire(ctx); err != nil {
		return err
	}
	defer d.release()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := markFTSDirty(ctx, tx); err != nil {
		return err
	}
	if err := upsertMessagesTx(ctx, tx, msgs); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertMessagesTx(ctx context.Context, tx *sql.Tx, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, upsertMessageSQL)
	if err != nil {
		return err
	}
	defer stmt.Close()
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		var body any
		if m.BodyFetched || m.HasBody {
			body = m.Body
		}
		if m.ToEmails == nil {
			m.ToEmails = []string{}
		}
		if m.CcEmails == nil {
			m.CcEmails = []string{}
		}
		if m.LabelIDs == nil {
			m.LabelIDs = []string{}
		}
		if m.SyncedAt.IsZero() {
			m.SyncedAt = time.Now().UTC()
		}
		_, err := stmt.ExecContext(ctx,
			m.ID, m.ThreadID, m.HistoryID, m.InternalDate.UTC(),
			m.FromName, m.FromEmail, encodeList(m.ToEmails), encodeList(m.CcEmails),
			m.Subject, m.Snippet, body, m.SizeBytes, encodeList(m.LabelIDs),
			m.IsRead, m.IsOutgoing, m.IsDeleted, m.HasBody, m.BodyFetched, m.SyncedAt.UTC(),
			encodeHeaders(m.Headers),
		)
		if err != nil {
			return err
		}
		ids = append(ids, m.ID)
	}
	return refreshSearchTextTx(ctx, tx, ids)
}

func (d *DB) GetMessage(ctx context.Context, id string) (Message, error) {
	const q = `
SELECT id, thread_id, history_id, internal_date,
       from_name, from_email, to_json(to_emails)::VARCHAR, to_json(cc_emails)::VARCHAR,
       subject, snippet, COALESCE(body, ''), size_bytes, to_json(label_ids)::VARCHAR,
       is_read, is_outgoing, is_deleted, has_body, body_fetched, synced_at,
       to_json(headers)::VARCHAR
FROM messages WHERE id = ?
`
	var m Message
	var toJSON, ccJSON, labelJSON string
	var historyID sql.NullInt64
	var headersJSON sql.NullString
	err := d.sql.QueryRowContext(ctx, q, id).Scan(
		&m.ID, &m.ThreadID, &historyID, &m.InternalDate,
		&m.FromName, &m.FromEmail, &toJSON, &ccJSON,
		&m.Subject, &m.Snippet, &m.Body, &m.SizeBytes, &labelJSON,
		&m.IsRead, &m.IsOutgoing, &m.IsDeleted, &m.HasBody, &m.BodyFetched, &m.SyncedAt,
		&headersJSON,
	)
	if err != nil {
		return Message{}, err
	}
	if historyID.Valid {
		m.HistoryID = uint64(historyID.Int64)
	}
	m.ToEmails = decodeList(toJSON)
	m.CcEmails = decodeList(ccJSON)
	m.LabelIDs = decodeList(labelJSON)
	m.Headers = decodeHeaders(headersJSON)
	return m, nil
}

func (d *DB) ListMessages(ctx context.Context, f ListFilter) ([]Message, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > maxLimit {
		f.Limit = maxLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	if f.Offset > maxOffset {
		f.Offset = maxOffset
	}
	q, err := search.Parse(f.Query)
	if err != nil {
		return nil, err
	}
	if q.Unread {
		f.Unread = true
	}
	if !q.After.IsZero() && f.After.IsZero() {
		f.After = q.After
	}
	if !q.Before.IsZero() && f.Before.IsZero() {
		f.Before = q.Before
	}

	rank := false
	if len(q.Terms) > 0 {
		ok, err := d.hasFTS(ctx)
		if err != nil {
			return nil, err
		}
		rank = ok
	}
	msgs, err := d.listMessages(ctx, f, q, rank)
	if err != nil && rank {
		return d.listMessages(ctx, f, q, false)
	}
	return msgs, err
}

func (d *DB) listMessages(ctx context.Context, f ListFilter, q search.Query, useFTS bool) ([]Message, error) {
	var b strings.Builder
	args := make([]any, 0, 16)
	b.WriteString(messageSelect)
	b.WriteString("FROM messages\nWHERE NOT is_deleted\n")
	if f.Unread {
		b.WriteString(" AND NOT is_read")
	}
	if f.From != "" {
		b.WriteString(" AND from_email = ?")
		args = append(args, f.From)
	}
	if q.From != "" {
		b.WriteString(" AND (from_email ILIKE ?" + likeEscape + " OR from_name ILIKE ?" + likeEscape + ")")
		pat := likeContains(q.From)
		args = append(args, pat, pat)
	}
	if q.To != "" {
		b.WriteString(" AND (array_to_string(to_emails, ' ') ILIKE ?" + likeEscape + " OR array_to_string(cc_emails, ' ') ILIKE ?" + likeEscape + ")")
		pat := likeContains(q.To)
		args = append(args, pat, pat)
	}
	if q.Subject != "" {
		b.WriteString(" AND subject ILIKE ?" + likeEscape)
		args = append(args, likeContains(q.Subject))
	}
	if f.Label != "" {
		b.WriteString(" AND list_contains(label_ids, ?)")
		args = append(args, f.Label)
	}
	if !f.After.IsZero() {
		b.WriteString(" AND internal_date >= ?")
		args = append(args, f.After.UTC())
	}
	if !f.Before.IsZero() {
		b.WriteString(" AND internal_date < ?")
		args = append(args, f.Before.UTC())
	}
	for _, term := range q.Terms {
		b.WriteString(" AND (")
		b.WriteString(searchTextSQL)
		b.WriteString(") ILIKE ?" + likeEscape)
		args = append(args, likeContains(term))
	}
	searchPage := f.Query != ""
	if !searchPage && !f.AfterDate.IsZero() && f.AfterID != "" {
		b.WriteString(" AND (internal_date < ? OR (internal_date = ? AND id < ?))")
		args = append(args, f.AfterDate.UTC(), f.AfterDate.UTC(), f.AfterID)
	}
	if useFTS && len(q.Terms) > 0 {
		b.WriteString(" ORDER BY COALESCE(fts_main_messages.match_bm25(id, ?), 0) DESC, internal_date DESC, id DESC LIMIT ?")
		args = append(args, strings.Join(q.Terms, " "), f.Limit)
	} else {
		b.WriteString(" ORDER BY internal_date DESC, id DESC LIMIT ?")
		args = append(args, f.Limit)
	}
	if searchPage && f.Offset > 0 {
		b.WriteString(" OFFSET ?")
		args = append(args, f.Offset)
	}

	rows, err := d.sql.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func (d *DB) ResetSeen(ctx context.Context) error {
	if err := d.acquire(ctx); err != nil {
		return err
	}
	defer d.release()
	_, err := d.sql.ExecContext(ctx, "DELETE FROM sync_seen")
	return err
}

func resetSeenTx(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM sync_seen")
	return err
}

func (d *DB) AddSeen(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := d.acquire(ctx); err != nil {
		return err
	}
	defer d.release()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := addSeenTx(ctx, tx, ids); err != nil {
		return err
	}
	return tx.Commit()
}

func addSeenTx(ctx context.Context, tx *sql.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, "INSERT OR IGNORE INTO sync_seen(id) VALUES (?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.ExecContext(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) MarkMissingDeleted(ctx context.Context) (int64, error) {
	if err := d.acquire(ctx); err != nil {
		return 0, err
	}
	defer d.release()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := markFTSDirty(ctx, tx); err != nil {
		return 0, err
	}
	n, err := markMissingDeletedTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

func markMissingDeletedTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	res, err := tx.ExecContext(ctx, `
UPDATE messages SET is_deleted = true
WHERE NOT is_deleted AND id NOT IN (SELECT id FROM sync_seen)
`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := resetSeenTx(ctx, tx); err != nil {
		return n, err
	}
	return n, nil
}

func (d *DB) MarkDeleted(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := d.acquire(ctx); err != nil {
		return err
	}
	defer d.release()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := markFTSDirty(ctx, tx); err != nil {
		return err
	}
	if err := markDeletedTx(ctx, tx, ids); err != nil {
		return err
	}
	return tx.Commit()
}

func markDeletedTx(ctx context.Context, tx *sql.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, "UPDATE messages SET is_deleted = true WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.ExecContext(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) UpdateLabels(ctx context.Context, id string, labels []string, isRead bool) error {
	if labels == nil {
		labels = []string{}
	}
	if err := d.acquire(ctx); err != nil {
		return err
	}
	defer d.release()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := markFTSDirty(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE messages SET label_ids = `+listSQL+`, is_read = ? WHERE id = ?
`, encodeList(labels), isRead, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) MissingIDs(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	have := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		var found string
		err := d.sql.QueryRowContext(ctx, "SELECT id FROM messages WHERE id = ?", id).Scan(&found)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		have[found] = struct{}{}
	}
	var missing []string
	for _, id := range ids {
		if _, ok := have[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

func (d *DB) UpsertLabels(ctx context.Context, labels []Label) error {
	if len(labels) == 0 {
		return nil
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO labels(id, name, type) VALUES (?, ?, ?)
ON CONFLICT (id) DO UPDATE SET name = excluded.name, type = excluded.type
`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, l := range labels {
		if _, err := stmt.ExecContext(ctx, l.ID, l.Name, l.Type); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) LabelMap(ctx context.Context) (map[string]string, error) {
	rows, err := d.sql.QueryContext(ctx, "SELECT id, name FROM labels")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

func (d *DB) SetState(ctx context.Context, key, value string) error {
	_, err := d.sql.ExecContext(ctx, setStateSQL, key, value)
	return err
}

const setStateSQL = `
INSERT INTO sync_state(key, value) VALUES (?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value
`

func setStateTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, setStateSQL, key, value)
	return err
}

func (d *DB) GetState(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := d.sql.QueryRowContext(ctx, "SELECT value FROM sync_state WHERE key = ?", key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (d *DB) ClearState(ctx context.Context, key string) error {
	_, err := d.sql.ExecContext(ctx, "DELETE FROM sync_state WHERE key = ?", key)
	return err
}

func clearStateTx(ctx context.Context, tx *sql.Tx, key string) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM sync_state WHERE key = ?", key)
	return err
}

func likeContains(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return "%" + s + "%"
}

func (d *DB) Coverage(ctx context.Context) (Coverage, error) {
	var c Coverage
	err := d.sql.QueryRowContext(ctx, `
SELECT coalesce(sum(CASE WHEN has_body THEN 1 ELSE 0 END), 0), count(*)
FROM messages WHERE NOT is_deleted
`).Scan(&c.WithBody, &c.Total)
	return c, err
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	var out []Message
	for rows.Next() {
		var m Message
		var toJSON, ccJSON, labelJSON string
		var historyID sql.NullInt64
		if err := rows.Scan(
			&m.ID, &m.ThreadID, &historyID, &m.InternalDate,
			&m.FromName, &m.FromEmail, &toJSON, &ccJSON,
			&m.Subject, &m.Snippet, &m.Body, &m.SizeBytes, &labelJSON,
			&m.IsRead, &m.IsOutgoing, &m.IsDeleted, &m.HasBody, &m.BodyFetched, &m.SyncedAt,
		); err != nil {
			return nil, err
		}
		if historyID.Valid {
			m.HistoryID = uint64(historyID.Int64)
		}
		m.ToEmails = decodeList(toJSON)
		m.CcEmails = decodeList(ccJSON)
		m.LabelIDs = decodeList(labelJSON)
		out = append(out, m)
	}
	return out, rows.Err()
}

func encodeList(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func encodeHeaders(h []Header) any {
	if h == nil {
		return nil
	}
	b, err := json.Marshal(h)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeHeaders(s sql.NullString) []Header {
	if !s.Valid || s.String == "" || s.String == "null" {
		return nil
	}
	var out []Header
	if err := json.Unmarshal([]byte(s.String), &out); err != nil {
		return nil
	}
	if out == nil {
		return []Header{}
	}
	return out
}

func decodeList(s string) []string {
	if s == "" || s == "null" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return []string{}
	}
	if out == nil {
		return []string{}
	}
	return out
}
