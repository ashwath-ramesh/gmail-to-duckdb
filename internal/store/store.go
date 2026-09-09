package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/search"
	_ "github.com/duckdb/duckdb-go/v2"
)

const (
	SchemaVersion      = 1
	StateSchemaVersion = "schema_version"
	StateLastSyncOK    = "last_sync_ok"
	StateLastSyncError = "last_sync_error"

	stateSearchText = "search_text_v1"
	stateFTSIndex   = "fts_index"
	ftsIndexVer     = "search_text"
	maxLimit        = 100
	maxOffset       = 5000
	maxQueryRunes   = 200
	likeEscape      = " ESCAPE '\\'"
)

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

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
  synced_at TIMESTAMP,
  search_text VARCHAR
);
CREATE INDEX IF NOT EXISTS messages_internal_date ON messages(internal_date);
CREATE INDEX IF NOT EXISTS messages_from_email ON messages(from_email);
CREATE INDEX IF NOT EXISTS messages_thread_id ON messages(thread_id);
CREATE INDEX IF NOT EXISTS messages_is_deleted ON messages(is_deleted);
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

const searchTextSQL = `trim(concat_ws(' ', from_name, from_email, array_to_string(to_emails, ' '), array_to_string(cc_emails, ' '), subject, snippet, body))`

const messageSelect = `
SELECT id, thread_id, history_id, internal_date,
       from_name, from_email, to_json(to_emails)::VARCHAR, to_json(cc_emails)::VARCHAR,
       subject, snippet, '', size_bytes, to_json(label_ids)::VARCHAR,
       is_read, is_outgoing, is_deleted, has_body, synced_at
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
	SyncedAt     time.Time
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
	sql   *sql.DB
	ftsOK bool
}

type Options struct {
	DuckUI  bool
	TempDir string
}

func Open(path string) (*DB, error) {
	return OpenWith(path, Options{})
}

func OpenWith(path string, opt Options) (*DB, error) {
	return openWith(path, opt, nil)
}

func openWith(path string, opt Options, wrap func(execer) execer) (*DB, error) {
	sqldb, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
	sqldb.SetMaxOpenConns(1)
	if _, err := sqldb.Exec(schema); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	db := &DB{sql: sqldb}
	ctx := context.Background()
	if err := db.migrateSearch(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := db.ensureSchemaVersion(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	var ex execer = sqldb
	if wrap != nil {
		ex = wrap(sqldb)
	}
	if err := bootstrapTrusted(ex, opt); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := db.lockSQL(opt); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
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

func (d *DB) lockSQL(opt Options) error {
	if opt.TempDir != "" {
		q := "SET temp_directory = '" + strings.ReplaceAll(opt.TempDir, "'", "''") + "'"
		if _, err := d.sql.Exec(q); err != nil {
			return fmt.Errorf("temp_directory: %w", err)
		}
	}
	if _, err := d.sql.Exec("SET enable_external_access = false"); err != nil {
		return fmt.Errorf("enable_external_access: %w", err)
	}
	if _, err := d.sql.Exec("SET lock_configuration = true"); err != nil {
		return fmt.Errorf("lock_configuration: %w", err)
	}
	return nil
}

func (d *DB) ensureSchemaVersion(ctx context.Context) error {
	_, ok, err := d.GetState(ctx, StateSchemaVersion)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	return d.SetState(ctx, StateSchemaVersion, fmt.Sprintf("%d", SchemaVersion))
}

func (d *DB) migrateSearch(ctx context.Context) error {
	if _, err := d.sql.Exec(`ALTER TABLE messages ADD COLUMN IF NOT EXISTS search_text VARCHAR`); err != nil {
		return fmt.Errorf("search_text: %w", err)
	}
	if _, ok, err := d.GetState(ctx, stateSearchText); err != nil {
		return err
	} else if ok {
		return nil
	}
	if _, err := d.sql.Exec(`UPDATE messages SET search_text = ` + searchTextSQL + ` WHERE search_text IS NULL OR search_text = ''`); err != nil {
		return fmt.Errorf("search_text backfill: %w", err)
	}
	return d.SetState(ctx, stateSearchText, "1")
}

func (d *DB) Close() error {
	return d.sql.Close()
}

func (d *DB) SQL() *sql.DB {
	return d.sql
}

func (d *DB) UpsertMessages(ctx context.Context, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	const q = `
INSERT INTO messages (
  id, thread_id, history_id, internal_date,
  from_name, from_email, to_emails, cc_emails,
  subject, snippet, body, size_bytes, label_ids,
  is_read, is_outgoing, is_deleted, has_body, synced_at
) VALUES (
  ?, ?, ?, ?,
  ?, ?, CAST(? AS VARCHAR[]), CAST(? AS VARCHAR[]),
  ?, ?, ?, ?, CAST(? AS VARCHAR[]),
  ?, ?, ?, ?, ?
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
  body = CASE WHEN excluded.has_body THEN excluded.body ELSE messages.body END,
  size_bytes = excluded.size_bytes,
  label_ids = excluded.label_ids,
  is_read = excluded.is_read,
  is_outgoing = excluded.is_outgoing,
  is_deleted = excluded.is_deleted,
  has_body = messages.has_body OR excluded.has_body,
  synced_at = excluded.synced_at
`
	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return err
	}
	defer stmt.Close()

	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		var body any
		if m.HasBody {
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
			m.IsRead, m.IsOutgoing, m.IsDeleted, m.HasBody, m.SyncedAt.UTC(),
		)
		if err != nil {
			return err
		}
		ids = append(ids, m.ID)
	}
	if err := refreshSearchTextTx(ctx, tx, ids); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) GetMessage(ctx context.Context, id string) (Message, error) {
	const q = `
SELECT id, thread_id, history_id, internal_date,
       from_name, from_email, to_json(to_emails)::VARCHAR, to_json(cc_emails)::VARCHAR,
       subject, snippet, COALESCE(body, ''), size_bytes, to_json(label_ids)::VARCHAR,
       is_read, is_outgoing, is_deleted, has_body, synced_at
FROM messages WHERE id = ?
`
	var m Message
	var toJSON, ccJSON, labelJSON string
	var historyID sql.NullInt64
	err := d.sql.QueryRowContext(ctx, q, id).Scan(
		&m.ID, &m.ThreadID, &historyID, &m.InternalDate,
		&m.FromName, &m.FromEmail, &toJSON, &ccJSON,
		&m.Subject, &m.Snippet, &m.Body, &m.SizeBytes, &labelJSON,
		&m.IsRead, &m.IsOutgoing, &m.IsDeleted, &m.HasBody, &m.SyncedAt,
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
	if n := utf8.RuneCountInString(f.Query); n > maxQueryRunes {
		f.Query = string([]rune(f.Query)[:maxQueryRunes])
	}
	q := search.Parse(f.Query)
	if q.Unread {
		f.Unread = true
	}
	if !q.After.IsZero() && f.After.IsZero() {
		f.After = q.After
	}
	if !q.Before.IsZero() && f.Before.IsZero() {
		f.Before = q.Before
	}

	hasFTS, err := d.hasFTS(ctx)
	if err != nil {
		return nil, err
	}
	short := utf8.RuneCountInString(q.Text) < 2
	if q.Text != "" && hasFTS {
		msgs, err := d.listMessages(ctx, f, q, true)
		if err != nil {
			if short {
				return nil, err
			}
		} else if len(msgs) > 0 || short {
			return msgs, nil
		}
	}
	return d.listMessages(ctx, f, q, false)
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
	if q.Text != "" {
		if useFTS {
			b.WriteString(" AND fts_main_messages.match_bm25(id, ?) IS NOT NULL")
			args = append(args, q.Text)
		} else {
			b.WriteString(" AND search_text ILIKE ?" + likeEscape)
			args = append(args, likeContains(q.Text))
		}
	}
	searchPage := f.Query != ""
	if !searchPage && !f.AfterDate.IsZero() && f.AfterID != "" {
		b.WriteString(" AND (internal_date < ? OR (internal_date = ? AND id < ?))")
		args = append(args, f.AfterDate.UTC(), f.AfterDate.UTC(), f.AfterID)
	}
	if useFTS && q.Text != "" {
		b.WriteString(" ORDER BY fts_main_messages.match_bm25(id, ?) DESC, internal_date DESC, id DESC LIMIT ?")
		args = append(args, q.Text, f.Limit)
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
	_, err := d.sql.ExecContext(ctx, "DELETE FROM sync_seen")
	return err
}

func (d *DB) AddSeen(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
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
	return tx.Commit()
}

func (d *DB) MarkMissingDeleted(ctx context.Context) (int64, error) {
	res, err := d.sql.ExecContext(ctx, `
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
	if err := d.ResetSeen(ctx); err != nil {
		return n, err
	}
	return n, nil
}

func (d *DB) MarkDeleted(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
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
	return tx.Commit()
}

func (d *DB) UpdateLabels(ctx context.Context, id string, labels []string, isRead bool) error {
	if labels == nil {
		labels = []string{}
	}
	_, err := d.sql.ExecContext(ctx, `
UPDATE messages SET label_ids = CAST(? AS VARCHAR[]), is_read = ? WHERE id = ?
`, encodeList(labels), isRead, id)
	return err
}

func (d *DB) UpdateBody(ctx context.Context, id, body string) error {
	_, err := d.sql.ExecContext(ctx, `
UPDATE messages SET body = ?, has_body = true, synced_at = ? WHERE id = ?
`, body, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	return refreshSearchTextTx(ctx, d.sql, []string{id})
}

func (d *DB) IDsWithoutBody(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := d.sql.QueryContext(ctx, `
SELECT id FROM messages WHERE NOT has_body AND NOT is_deleted LIMIT ?
`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
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
	_, err := d.sql.ExecContext(ctx, `
INSERT INTO sync_state(key, value) VALUES (?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value
`, key, value)
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

func (d *DB) RebuildFTS(ctx context.Context) error {
	_, err := d.sql.ExecContext(ctx, `
PRAGMA create_fts_index('messages', 'id', 'search_text', overwrite=1)
`)
	if err != nil {
		return fmt.Errorf("fts: %w", err)
	}
	if err := d.SetState(ctx, stateFTSIndex, ftsIndexVer); err != nil {
		return err
	}
	d.ftsOK = true
	return nil
}

func (d *DB) EnsureFTS(ctx context.Context) error {
	ok, err := d.hasFTS(ctx)
	if err != nil {
		return err
	}
	ver, _, err := d.GetState(ctx, stateFTSIndex)
	if err != nil {
		return err
	}
	if ok && ver == ftsIndexVer {
		return nil
	}
	return d.RebuildFTS(ctx)
}

func likeContains(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return "%" + s + "%"
}

func refreshSearchTextTx(ctx context.Context, ex execer, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("UPDATE messages SET search_text = ")
	b.WriteString(searchTextSQL)
	b.WriteString(" WHERE id IN (")
	args := make([]any, len(ids))
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('?')
		args[i] = id
	}
	b.WriteByte(')')
	_, err := ex.ExecContext(ctx, b.String(), args...)
	return err
}

func (d *DB) hasFTS(ctx context.Context) (bool, error) {
	if d.ftsOK {
		return true, nil
	}
	var n int
	err := d.sql.QueryRowContext(ctx, `
SELECT count(*) FROM duckdb_schemas() WHERE schema_name = 'fts_main_messages'
`).Scan(&n)
	if err != nil {
		return false, err
	}
	ver, _, err := d.GetState(ctx, stateFTSIndex)
	if err != nil {
		return false, err
	}
	ok := n > 0 && ver == ftsIndexVer
	if ok {
		d.ftsOK = true
	}
	return ok, nil
}

func (d *DB) HasFTS(ctx context.Context) (bool, error) {
	return d.hasFTS(ctx)
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
			&m.IsRead, &m.IsOutgoing, &m.IsDeleted, &m.HasBody, &m.SyncedAt,
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
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
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
