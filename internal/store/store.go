package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
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

// messageSelect projects list rows. Body stays empty so DuckDB can prune it.
const messageSelect = `
SELECT messages.id, messages.thread_id, messages.history_id, messages.internal_date,
       messages.from_name, messages.from_email, to_json(messages.to_emails)::VARCHAR, to_json(messages.cc_emails)::VARCHAR,
       messages.subject, messages.snippet, '', messages.size_bytes, to_json(messages.label_ids)::VARCHAR,
       messages.is_read, messages.is_outgoing, messages.is_deleted, messages.has_body, messages.body_fetched, messages.synced_at
`

// messageLookupCols are the typed columns messageSelect, filters, and order need.
const messageLookupCols = `messages.id, messages.thread_id, messages.history_id, messages.internal_date,
       messages.from_name, messages.from_email, messages.to_emails, messages.cc_emails,
       messages.subject, messages.snippet, messages.size_bytes, messages.label_ids,
       messages.is_read, messages.is_outgoing, messages.is_deleted, messages.has_body,
       messages.body_fetched, messages.synced_at`

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
	sql           *sql.DB
	writer        *sql.Conn
	maint         *sql.Conn
	mu            chan struct{}
	path          string
	spill         string
	duckUI        bool
	settingsReady atomic.Bool
	idx           *searchCache
	indexMu       sync.Mutex
	maintCh       chan struct{}
	maintWG       sync.WaitGroup
	maintCancel   context.CancelFunc
	maintStarted  atomic.Bool
	building      atomic.Bool
	IndexLog      func(string, ...any)
	testHoldDuck  func()
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
	err := d.withTx(ctx, func(tx *sql.Tx) error {
		return upsertMessagesTx(ctx, tx, msgs)
	})
	if err == nil {
		d.notifyIndex()
	}
	return err
}

type searchStamp struct {
	date    time.Time
	deleted bool
}

func upsertMessagesTx(ctx context.Context, tx *sql.Tx, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(msgs))
	byID := make(map[string]Message, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
		byID[m.ID] = m
	}
	prev, err := loadSearchStamps(ctx, tx, ids)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, upsertMessageSQL)
	if err != nil {
		return err
	}
	defer stmt.Close()
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
	}
	stale, err := staleSearchIDs(ctx, tx, ids)
	if err != nil {
		return err
	}
	if err := touchSearchTx(ctx, tx, stale); err != nil {
		return err
	}
	touched := make(map[string]struct{}, len(stale))
	for _, id := range stale {
		touched[id] = struct{}{}
	}
	var revOnly []string
	for _, id := range ids {
		if _, ok := touched[id]; ok {
			continue
		}
		was, ok := prev[id]
		if !ok {
			continue
		}
		cur := byID[id]
		if !was.date.Equal(cur.InternalDate.UTC()) || was.deleted != cur.IsDeleted {
			revOnly = append(revOnly, id)
		}
	}
	return touchRevisionTx(ctx, tx, revOnly)
}

func loadSearchStamps(ctx context.Context, tx *sql.Tx, ids []string) (map[string]searchStamp, error) {
	out := make(map[string]searchStamp, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var b strings.Builder
	b.WriteString("SELECT id, internal_date, is_deleted FROM messages WHERE id IN ")
	args := writeIn(&b, nil, ids)
	rows, err := tx.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var st searchStamp
		if err := rows.Scan(&id, &st.date, &st.deleted); err != nil {
			return nil, err
		}
		st.date = st.date.UTC()
		out[id] = st
	}
	return out, rows.Err()
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

	return d.searchMessages(ctx, f, q)
}

func (d *DB) ResetSeen(ctx context.Context) error {
	return d.withTx(ctx, func(tx *sql.Tx) error {
		return resetSeenTx(ctx, tx)
	})
}

func resetSeenTx(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM sync_seen")
	return err
}

func (d *DB) AddSeen(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return d.withTx(ctx, func(tx *sql.Tx) error {
		return addSeenTx(ctx, tx, ids)
	})
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
	var n int64
	err := d.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = markMissingDeletedTx(ctx, tx)
		return err
	})
	if err == nil && n > 0 {
		d.notifyIndex()
	}
	return n, err
}

func markMissingDeletedTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	var n int64
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM messages
WHERE NOT is_deleted AND id NOT IN (SELECT id FROM sync_seen)
`).Scan(&n); err != nil {
		return 0, err
	}
	if n > 0 {
		rev, err := nextCorpusTx(ctx, tx)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE messages
SET is_deleted = true, search_revision = ?
WHERE NOT is_deleted AND id NOT IN (SELECT id FROM sync_seen)
`, rev); err != nil {
			return 0, err
		}
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
	err := d.withTx(ctx, func(tx *sql.Tx) error {
		return markDeletedTx(ctx, tx, ids)
	})
	if err == nil {
		d.notifyIndex()
	}
	return err
}

func markDeletedTx(ctx context.Context, tx *sql.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	rev, err := nextCorpusTx(ctx, tx)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("UPDATE messages SET is_deleted = true, search_revision = ? WHERE id IN ")
	args := writeIn(&b, []any{rev}, ids)
	_, err = tx.ExecContext(ctx, b.String(), args...)
	return err
}

func (d *DB) UpdateLabels(ctx context.Context, id string, labels []string, isRead bool) error {
	if labels == nil {
		labels = []string{}
	}
	return d.withTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
UPDATE messages SET label_ids = `+listSQL+`, is_read = ? WHERE id = ?
`, encodeList(labels), isRead, id)
		return err
	})
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
	return d.withTx(ctx, func(tx *sql.Tx) error {
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
		return nil
	})
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
	if d.writer == nil {
		_, err := d.sql.ExecContext(ctx, setStateSQL, key, value)
		return err
	}
	return d.withTx(ctx, func(tx *sql.Tx) error {
		return setStateTx(ctx, tx, key, value)
	})
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
	if d.writer == nil {
		_, err := d.sql.ExecContext(ctx, "DELETE FROM sync_state WHERE key = ?", key)
		return err
	}
	return d.withTx(ctx, func(tx *sql.Tx) error {
		return clearStateTx(ctx, tx, key)
	})
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
