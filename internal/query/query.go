package query

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/htmlutil"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

const defaultLimit = 50

type Access interface {
	Status(context.Context) (Envelope, error)
	Search(context.Context, string) (Envelope, error)
	Get(context.Context, string, bool) (Envelope, error)
	Schema(context.Context) (Envelope, error)
	SQL(context.Context, string, bool) (Envelope, error)
}

type Envelope struct {
	SchemaVersion    int          `json:"schema_version"`
	LastSync         string       `json:"last_sync"`
	BodyCoverage     BodyCoverage `json:"body_coverage"`
	ResultCount      int          `json:"result_count"`
	Truncated        bool         `json:"truncated"`
	UntrustedContent bool         `json:"untrusted_content"`
	UntrustedFields  []string     `json:"untrusted_fields,omitempty"`
	FTS              bool         `json:"fts"`
	DuckDBUI         bool         `json:"duckdb_ui,omitempty"`
	Phase            string       `json:"phase,omitempty"`
	Processed        int          `json:"processed,omitempty"`
	LastError        string       `json:"last_error,omitempty"`
	Messages         []Message    `json:"messages,omitzero"`
	Message          *Message     `json:"message,omitempty"`
	Schema           *Schema      `json:"schema,omitempty"`
	SQL              *SQLPayload  `json:"sql,omitempty"`
	Checks           []Check      `json:"checks,omitempty"`
}

type BodyCoverage struct {
	WithBody     int    `json:"with_body"`
	Total        int    `json:"total"`
	SearchCovers string `json:"search_covers"`
}

type Message struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"thread_id"`
	InternalDate string   `json:"internal_date"`
	FromName     string   `json:"from_name"`
	FromEmail    string   `json:"from_email"`
	ToEmails     []string `json:"to_emails"`
	Subject      string   `json:"subject"`
	Snippet      string   `json:"snippet"`
	Body         string   `json:"body,omitempty"`
	LabelIDs     []string `json:"label_ids"`
	IsRead       bool     `json:"is_read"`
	IsOutgoing   bool     `json:"is_outgoing"`
	HasBody      bool     `json:"has_body"`
	IsHTML       bool     `json:"is_html,omitempty"`
}

type Schema struct {
	Version int                 `json:"version"`
	Tables  []store.TableSchema `json:"tables"`
}

type SQLPayload struct {
	Columns     []string `json:"columns"`
	ColumnTypes []string `json:"column_types"`
	Rows        [][]any  `json:"rows"`
}

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type Local struct {
	DB *store.DB
}

func (l Local) Status(ctx context.Context) (Envelope, error) { return Status(ctx, l.DB) }
func (l Local) Search(ctx context.Context, q string) (Envelope, error) {
	return Search(ctx, l.DB, q)
}
func (l Local) Get(ctx context.Context, id string, body bool) (Envelope, error) {
	return Get(ctx, l.DB, id, body)
}
func (l Local) Schema(ctx context.Context) (Envelope, error) { return SchemaInfo(ctx, l.DB) }
func (l Local) SQL(ctx context.Context, q string, write bool) (Envelope, error) {
	return SQL(ctx, l.DB, q, write)
}

func Status(ctx context.Context, db *store.DB) (Envelope, error) {
	env, err := meta(ctx, db)
	if err != nil {
		return Envelope{}, err
	}
	env.Phase = "idle"
	return env, nil
}

func Search(ctx context.Context, db *store.DB, q string) (Envelope, error) {
	env, err := meta(ctx, db)
	if err != nil {
		return Envelope{}, err
	}
	msgs, err := db.ListMessages(ctx, store.ListFilter{Query: q, Limit: defaultLimit})
	if err != nil {
		return Envelope{}, err
	}
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, MessageFromStore(m, false))
	}
	env.Messages = out
	env.ResultCount = len(out)
	env.Truncated = len(out) == defaultLimit
	MarkMailUntrusted(&env)
	return env, nil
}

func Get(ctx context.Context, db *store.DB, id string, includeBody bool) (Envelope, error) {
	env, err := meta(ctx, db)
	if err != nil {
		return Envelope{}, err
	}
	m, err := db.GetMessage(ctx, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return Envelope{}, fmt.Errorf("message %s not found", id)
		}
		return Envelope{}, err
	}
	msg := MessageFromStore(m, includeBody)
	env.Message = &msg
	env.ResultCount = 1
	MarkMailUntrusted(&env)
	return env, nil
}

func SchemaInfo(ctx context.Context, db *store.DB) (Envelope, error) {
	env, err := meta(ctx, db)
	if err != nil {
		return Envelope{}, err
	}
	tables, err := db.DescribeSchema(ctx)
	if err != nil {
		return Envelope{}, err
	}
	env.Schema = &Schema{Version: store.SchemaVersion, Tables: tables}
	env.ResultCount = len(tables)
	return env, nil
}

func SQL(ctx context.Context, db *store.DB, query string, allowWrite bool) (Envelope, error) {
	env, err := meta(ctx, db)
	if err != nil {
		return Envelope{}, err
	}
	res, err := db.QuerySQL(ctx, query, allowWrite)
	if err != nil {
		return Envelope{}, err
	}
	env.SQL = &SQLPayload{Columns: res.Columns, ColumnTypes: res.ColumnTypes, Rows: res.Rows}
	env.ResultCount = len(res.Rows)
	env.UntrustedContent = true
	env.UntrustedFields = append([]string{}, res.Columns...)
	return env, nil
}

var mailTextFields = []string{"subject", "snippet", "body", "from_name", "from_email", "to_emails", "cc_emails", "search_text"}

func MarkMailUntrusted(env *Envelope) {
	env.UntrustedContent = true
	env.UntrustedFields = append([]string{}, mailTextFields...)
}

func MessageFromStore(m store.Message, includeBody bool) Message {
	out := Message{
		ID:           m.ID,
		ThreadID:     m.ThreadID,
		InternalDate: m.InternalDate.UTC().Format(time.RFC3339),
		FromName:     m.FromName,
		FromEmail:    m.FromEmail,
		ToEmails:     m.ToEmails,
		Subject:      m.Subject,
		Snippet:      m.Snippet,
		LabelIDs:     m.LabelIDs,
		IsRead:       m.IsRead,
		IsOutgoing:   m.IsOutgoing,
		HasBody:      m.HasBody,
	}
	if includeBody {
		body := m.Body
		isHTML := htmlutil.LooksLikeHTML(body)
		if isHTML {
			body = htmlutil.Sanitize(body)
		}
		out.Body = body
		out.IsHTML = isHTML
	}
	return out
}

func meta(ctx context.Context, db *store.DB) (Envelope, error) {
	env := Envelope{SchemaVersion: store.SchemaVersion}
	if v, ok, err := db.GetState(ctx, store.StateSchemaVersion); err != nil {
		return Envelope{}, err
	} else if ok {
		if n, err := strconv.Atoi(v); err == nil {
			env.SchemaVersion = n
		}
	}
	if v, ok, err := db.GetState(ctx, store.StateLastSyncOK); err != nil {
		return Envelope{}, err
	} else if ok {
		env.LastSync = v
	}
	if v, ok, err := db.GetState(ctx, store.StateLastSyncError); err != nil {
		return Envelope{}, err
	} else if ok {
		env.LastError = v
	}
	c, err := db.Coverage(ctx)
	if err != nil {
		return Envelope{}, err
	}
	env.BodyCoverage = BodyCoverage{
		WithBody:     c.WithBody,
		Total:        c.Total,
		SearchCovers: c.SearchCovers(),
	}
	if ok, err := db.HasFTS(ctx); err == nil {
		env.FTS = ok
	}
	return env, nil
}

func FormatHuman(env Envelope) string {
	var b strings.Builder
	fmt.Fprintf(&b, "schema_version %d\n", env.SchemaVersion)
	if env.LastSync != "" {
		fmt.Fprintf(&b, "last_sync %s\n", env.LastSync)
	}
	fmt.Fprintf(&b, "bodies %d/%d (%s)\n", env.BodyCoverage.WithBody, env.BodyCoverage.Total, env.BodyCoverage.SearchCovers)
	if env.Phase != "" {
		fmt.Fprintf(&b, "phase %s\n", env.Phase)
	}
	if env.LastError != "" {
		fmt.Fprintf(&b, "last_error %s\n", env.LastError)
	}
	if env.Message != nil {
		fmt.Fprintf(&b, "id %s\nthread_id %s\nfrom %s\nsubject %s\n", env.Message.ID, env.Message.ThreadID, env.Message.FromEmail, env.Message.Subject)
		if env.Message.Body != "" {
			fmt.Fprintf(&b, "\n%s\n", env.Message.Body)
		} else if env.Message.Snippet != "" {
			fmt.Fprintf(&b, "snippet %s\n", env.Message.Snippet)
		}
	}
	for _, m := range env.Messages {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", m.ID, m.FromEmail, m.Subject)
	}
	if env.Schema != nil {
		for _, t := range env.Schema.Tables {
			fmt.Fprintf(&b, "%s\n", t.Name)
			for _, c := range t.Columns {
				fmt.Fprintf(&b, "  %s %s\n", c.Name, c.Type)
			}
		}
	}
	for _, c := range env.Checks {
		mark := "ok"
		if !c.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "%s %s %s\n", mark, c.Name, c.Detail)
	}
	return b.String()
}
