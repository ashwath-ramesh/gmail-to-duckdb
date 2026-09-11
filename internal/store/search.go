package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/search"
)

const verifyBatch = 256

var errIndexSkip = errors.New("search index unused")

func (d *DB) searchMessages(ctx context.Context, f ListFilter, q search.Query) ([]Message, error) {
	if len(q.Terms) == 0 {
		return d.queryList(ctx, nil, f, q, nil, true, f.Limit, f.Offset, false, time.Time{}, "")
	}
	msgs, err := d.indexedSearch(ctx, f, q)
	if err == nil {
		return msgs, nil
	}
	if !errors.Is(err, errIndexSkip) {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return d.literalSearch(ctx, f, q)
}

func (d *DB) indexedSearch(ctx context.Context, f ListFilter, q search.Query) ([]Message, error) {
	if d.idx == nil {
		return nil, errIndexSkip
	}
	lowers, err := d.lowerTerms(ctx, q.Terms)
	if err != nil {
		return nil, err
	}
	var parts []string
	for _, t := range lowers {
		if strings.IndexByte(t, 0) >= 0 {
			return nil, errIndexSkip
		}
		grams := trigrams(t)
		if len(grams) == 0 {
			continue
		}
		part, ok := ftsMatch(grams)
		if !ok {
			return nil, errIndexSkip
		}
		parts = append(parts, "("+part+")")
	}
	if len(parts) == 0 {
		return nil, errIndexSkip
	}
	cands, meta, err := d.idx.snapshot(ctx, strings.Join(parts, " AND "))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !transientIndexErr(err) {
			d.idx.markBroken()
			d.notifyIndex()
		}
		return nil, errIndexSkip
	}

	duck, err := d.sql.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer duck.Close()
	if _, err := duck.ExecContext(ctx, "BEGIN TRANSACTION READ ONLY"); err != nil {
		return nil, err
	}
	defer func() {
		if err := rollbackConn(duck); err != nil {
			discardConn(duck)
		}
	}()

	// SQLite candidates are already collected and unpinned. DuckDB verify uses
	// one read-only txn. The first real table read (freshness/sync_state) pins
	// that DuckDB snapshot for the whole database, not per table. Watermark W
	// means the index covers revision<=W; this txn adds revision>W.
	fresh, err := d.readFreshness(ctx, duck)
	if err != nil {
		return nil, err
	}
	if d.testHoldDuck != nil {
		d.testHoldDuck()
	}
	if fresh.accelOff || fresh.repair || fresh.identity != meta.Identity || fresh.epoch != meta.Epoch || meta.Version != searchIndexVer || !meta.Ready || meta.Watermark > fresh.corpus {
		return nil, errIndexSkip
	}

	delta, err := loadDeltaCands(ctx, duck, meta.Watermark)
	if err != nil {
		return nil, err
	}
	merged := mergeCands(cands, delta)
	need := f.Offset + f.Limit
	var hits []Message
	for start := 0; start < len(merged) && len(hits) < need; start += verifyBatch {
		end := start + verifyBatch
		if end > len(merged) {
			end = len(merged)
		}
		ids := make([]string, 0, end-start)
		for _, c := range merged[start:end] {
			ids = append(ids, c.ID)
		}
		page, err := d.queryList(ctx, duck, f, q, q.Terms, !fresh.repair, len(ids), 0, false, time.Time{}, "", ids...)
		if err != nil {
			return nil, err
		}
		seen := make(map[string]Message, len(page))
		for _, m := range page {
			seen[m.ID] = m
		}
		for _, c := range merged[start:end] {
			if m, ok := seen[c.ID]; ok {
				hits = append(hits, m)
			}
		}
	}
	if f.Offset >= len(hits) {
		return []Message{}, nil
	}
	end := f.Offset + f.Limit
	if end > len(hits) {
		end = len(hits)
	}
	return hits[f.Offset:end], nil
}

func (d *DB) literalSearch(ctx context.Context, f ListFilter, q search.Query) ([]Message, error) {
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN TRANSACTION READ ONLY"); err != nil {
		return nil, err
	}
	defer func() {
		if err := rollbackConn(conn); err != nil {
			discardConn(conn)
		}
	}()
	fresh, err := d.readFreshness(ctx, conn)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return d.queryList(ctx, conn, f, q, q.Terms, !fresh.repair, f.Limit, f.Offset, false, time.Time{}, "")
}

func (d *DB) queryList(ctx context.Context, conn *sql.Conn, f ListFilter, q search.Query, terms []string, useCachedText bool, limit, offset int, keyed bool, afterDate time.Time, afterID string, ids ...string) ([]Message, error) {
	query, args := buildListQuery(f, q, terms, useCachedText, limit, offset, keyed, afterDate, afterID, ids...)
	var (
		rows *sql.Rows
		err  error
	)
	if conn != nil {
		rows, err = conn.QueryContext(ctx, query, args...)
	} else {
		rows, err = d.sql.QueryContext(ctx, query, args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

func buildListQuery(f ListFilter, q search.Query, terms []string, useCachedText bool, limit, offset int, keyed bool, afterDate time.Time, afterID string, ids ...string) (string, []any) {
	var b strings.Builder
	args := make([]any, 0, 16+len(ids))
	textSQL := searchTextSQL
	if useCachedText {
		textSQL = "messages.search_text"
	}
	b.WriteString(messageSelect)
	if len(ids) > 0 {
		// LIMIT len(ids) is a semantic barrier. Unique candidate IDs plus the
		// messages PK keep this at most the batch. DuckDB can still push
		// outer ILIKE into a MATERIALIZED CTE; it must not run that filter
		// on the full table scan.
		b.WriteString("FROM (\nSELECT ")
		b.WriteString(messageLookupCols)
		b.WriteString(", (")
		b.WriteString(textSQL)
		b.WriteString(") AS search_text\nFROM (SELECT UNNEST(")
		b.WriteString(listSQL)
		b.WriteString(") AS id) c\nJOIN messages ON messages.id = c.id\n")
		b.WriteString("WHERE NOT messages.is_deleted\nLIMIT ?\n) messages\nWHERE TRUE\n")
		args = append(args, encodeList(ids), len(ids))
		textSQL = "messages.search_text"
	} else {
		b.WriteString("FROM messages\nWHERE NOT messages.is_deleted\n")
	}
	args = appendFilters(&b, args, f, q)
	for _, term := range terms {
		b.WriteString(" AND (")
		b.WriteString(textSQL)
		b.WriteString(") ILIKE ?" + likeEscape)
		args = append(args, likeContains(term))
	}
	searchPage := f.Query != ""
	if !searchPage && !f.AfterDate.IsZero() && f.AfterID != "" {
		b.WriteString(" AND (messages.internal_date < ? OR (messages.internal_date = ? AND messages.id < ?))")
		args = append(args, f.AfterDate.UTC(), f.AfterDate.UTC(), f.AfterID)
	}
	if keyed {
		b.WriteString(" AND (messages.internal_date < ? OR (messages.internal_date = ? AND messages.id < ?))")
		args = append(args, afterDate.UTC(), afterDate.UTC(), afterID)
	}
	b.WriteString(" ORDER BY messages.internal_date DESC, messages.id DESC LIMIT ?")
	args = append(args, limit)
	if searchPage && offset > 0 && len(ids) == 0 && !keyed {
		b.WriteString(" OFFSET ?")
		args = append(args, offset)
	}
	return b.String(), args
}

func appendFilters(b *strings.Builder, args []any, f ListFilter, q search.Query) []any {
	if f.Unread {
		b.WriteString(" AND NOT messages.is_read")
	}
	if f.From != "" {
		b.WriteString(" AND messages.from_email = ?")
		args = append(args, f.From)
	}
	if q.From != "" {
		b.WriteString(" AND (messages.from_email ILIKE ?" + likeEscape + " OR messages.from_name ILIKE ?" + likeEscape + ")")
		pat := likeContains(q.From)
		args = append(args, pat, pat)
	}
	if q.To != "" {
		b.WriteString(" AND (array_to_string(messages.to_emails, ' ') ILIKE ?" + likeEscape + " OR array_to_string(messages.cc_emails, ' ') ILIKE ?" + likeEscape + ")")
		pat := likeContains(q.To)
		args = append(args, pat, pat)
	}
	if q.Subject != "" {
		b.WriteString(" AND messages.subject ILIKE ?" + likeEscape)
		args = append(args, likeContains(q.Subject))
	}
	if f.Label != "" {
		b.WriteString(" AND list_contains(messages.label_ids, ?)")
		args = append(args, f.Label)
	}
	if !f.After.IsZero() {
		b.WriteString(" AND messages.internal_date >= ?")
		args = append(args, f.After.UTC())
	}
	if !f.Before.IsZero() {
		b.WriteString(" AND messages.internal_date < ?")
		args = append(args, f.Before.UTC())
	}
	return args
}

func (d *DB) lowerTerms(ctx context.Context, terms []string) ([]string, error) {
	if len(terms) == 0 {
		return nil, nil
	}
	var b strings.Builder
	b.WriteString("SELECT ")
	args := make([]any, len(terms))
	for i, t := range terms {
		args[i] = t
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("lower(?)")
	}
	row := d.sql.QueryRowContext(ctx, b.String(), args...)
	out := make([]string, len(terms))
	dest := make([]any, len(terms))
	for i := range out {
		dest[i] = &out[i]
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	return out, nil
}

func loadDeltaCands(ctx context.Context, conn *sql.Conn, watermark int64) ([]cand, error) {
	rows, err := conn.QueryContext(ctx, `
SELECT id, CAST(epoch_us(internal_date) AS BIGINT)
FROM messages
WHERE search_revision > ?
`, watermark)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.ID, &c.Date); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func mergeCands(a, b []cand) []cand {
	seen := make(map[string]cand, len(a)+len(b))
	for _, c := range a {
		seen[c.ID] = c
	}
	for _, c := range b {
		seen[c.ID] = c
	}
	out := make([]cand, 0, len(seen))
	for _, c := range seen {
		out = append(out, c)
	}
	slices.SortFunc(out, func(x, y cand) int {
		if x.Date != y.Date {
			if x.Date > y.Date {
				return -1
			}
			return 1
		}
		if x.ID > y.ID {
			return -1
		}
		if x.ID < y.ID {
			return 1
		}
		return 0
	})
	return out
}
