package store

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
)

var writeSQL = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "CREATE": true,
	"DROP": true, "ALTER": true, "COPY": true, "ATTACH": true,
	"CALL": true, "PRAGMA": true, "INSTALL": true, "LOAD": true,
	"MERGE": true, "TRUNCATE": true, "REPLACE": true, "SET": true,
	"VACUUM": true, "EXPORT": true, "IMPORT": true, "DETACH": true,
	"CHECKPOINT": true, "BEGIN": true, "COMMIT": true, "ROLLBACK": true,
	"EXECUTE": true, "FORCE": true, "PREPARE": true, "DEALLOCATE": true,
	"USE": true, "RESET": true,
}

var readSQL = map[string]bool{
	"SELECT": true, "WITH": true, "EXPLAIN": true, "DESCRIBE": true,
	"DESC": true, "SHOW": true, "SUMMARIZE": true, "PIVOT": true,
	"FROM": true, "VALUES": true, "TABLE": true,
}

var fileReadSQL = map[string]bool{
	"READ_CSV": true, "READ_CSV_AUTO": true, "READ_JSON": true,
	"READ_JSON_AUTO": true, "READ_PARQUET": true, "READ_TEXT": true,
	"READ_BLOB": true, "READ_XLSX": true,
}

func (d *DB) ExecSQL(ctx context.Context, query string) (SQLResult, error) {
	rows, err := d.sql.QueryContext(ctx, query)
	if err != nil {
		return SQLResult{}, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return SQLResult{}, err
	}
	types := make([]string, len(cols))
	if cts, err := rows.ColumnTypes(); err == nil {
		for i, ct := range cts {
			types[i] = ct.DatabaseTypeName()
			if types[i] == "" && ct.ScanType() != nil {
				types[i] = ct.ScanType().String()
			}
		}
	}
	res := SQLResult{Columns: cols, ColumnTypes: types, Rows: [][]any{}}
	for rows.Next() {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return SQLResult{}, err
		}
		line := make([]any, len(cols))
		for i, v := range raw {
			line[i] = cellValue(v)
		}
		res.Rows = append(res.Rows, line)
	}
	return res, rows.Err()
}

func (d *DB) QuerySQL(ctx context.Context, query string, allowWrite bool) (SQLResult, error) {
	if err := CheckSQL(query, allowWrite); err != nil {
		return SQLResult{}, err
	}
	if !allowWrite {
		if _, err := d.sql.ExecContext(ctx, "SET enable_external_access = false"); err == nil {
			defer func() { _, _ = d.sql.Exec("SET enable_external_access = true") }()
		}
	}
	return d.ExecSQL(ctx, query)
}

func CheckSQL(query string, allowWrite bool) error {
	kw, err := firstSQLKeyword(query)
	if err != nil {
		return err
	}
	if allowWrite {
		return nil
	}
	if writeSQL[kw] {
		return fmt.Errorf("write SQL requires --write: %s", kw)
	}
	if !readSQL[kw] {
		return fmt.Errorf("write SQL requires --write: %s", kw)
	}
	for _, tok := range sqlTokens(stripSQLComments(query)) {
		u := strings.ToUpper(tok)
		if writeSQL[u] {
			return fmt.Errorf("write SQL requires --write: %s", u)
		}
		if fileReadSQL[u] {
			return fmt.Errorf("write SQL requires --write: %s", u)
		}
	}
	return nil
}

func sqlTokens(s string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		out = append(out, b.String())
		b.Reset()
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\'' || c == '"' {
			flush()
			quote := c
			i++
			for i < len(s) {
				if s[i] == quote {
					if i+1 < len(s) && s[i+1] == quote {
						i += 2
						continue
					}
					break
				}
				i++
			}
			continue
		}
		if isSQLIdent(c) {
			b.WriteByte(c)
			continue
		}
		flush()
	}
	flush()
	return out
}

func isSQLIdent(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func (r SQLResult) StringRows() [][]string {
	out := make([][]string, len(r.Rows))
	for i, row := range r.Rows {
		line := make([]string, len(row))
		for j, v := range row {
			if v == nil {
				line[j] = ""
				continue
			}
			line[j] = fmt.Sprint(v)
		}
		out[i] = line
	}
	return out
}

func firstSQLKeyword(query string) (string, error) {
	q := stripSQLComments(query)
	q = strings.TrimSpace(q)
	if q == "" {
		return "", fmt.Errorf("empty query")
	}
	if i := strings.IndexByte(q, ';'); i >= 0 && strings.TrimSpace(q[i+1:]) != "" {
		return "", fmt.Errorf("multiple SQL statements are not allowed")
	}
	q = strings.TrimSuffix(strings.TrimSpace(q), ";")
	word := firstWord(q)
	if word == "" {
		return "", fmt.Errorf("empty query")
	}
	return strings.ToUpper(word), nil
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	for i, r := range s {
		if unicode.IsSpace(r) || r == '(' {
			return s[:i]
		}
	}
	return s
}

func stripSQLComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			b.WriteByte(c)
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					b.WriteByte(s[i+1])
					i++
					continue
				}
				inStr = false
			}
			continue
		}
		if c == '\'' {
			inStr = true
			b.WriteByte(c)
			continue
		}
		if c == '-' && i+1 < len(s) && s[i+1] == '-' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			if i < len(s) {
				b.WriteByte('\n')
			}
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			i++
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func cellValue(v any) any {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case []byte:
		return string(t)
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	default:
		return t
	}
}

type TableSchema struct {
	Name    string         `json:"name"`
	Columns []ColumnSchema `json:"columns"`
}

type ColumnSchema struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func (d *DB) DescribeSchema(ctx context.Context) ([]TableSchema, error) {
	rows, err := d.sql.QueryContext(ctx, `
SELECT table_name, column_name, data_type
FROM information_schema.columns
WHERE table_schema = 'main'
  AND table_name IN ('messages', 'labels', 'sync_state', 'sync_seen')
ORDER BY table_name, ordinal_position
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byName := map[string]*TableSchema{}
	var order []string
	for rows.Next() {
		var table, col, typ string
		if err := rows.Scan(&table, &col, &typ); err != nil {
			return nil, err
		}
		t, ok := byName[table]
		if !ok {
			t = &TableSchema{Name: table}
			byName[table] = t
			order = append(order, table)
		}
		t.Columns = append(t.Columns, ColumnSchema{Name: col, Type: typ})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]TableSchema, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	return out, nil
}
