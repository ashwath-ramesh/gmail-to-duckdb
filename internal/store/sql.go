package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"
)

// ExecSQL runs trusted embedded SQL only. User SQL must use QuerySQL.
func (d *DB) ExecSQL(ctx context.Context, query string) (SQLResult, error) {
	rows, err := d.sql.QueryContext(ctx, query)
	if err != nil {
		return SQLResult{}, err
	}
	defer rows.Close()
	return scanSQLResult(rows)
}

func (d *DB) QuerySQL(ctx context.Context, query string, allowWrite bool) (SQLResult, error) {
	if err := rejectSQLText(query); err != nil {
		return SQLResult{}, err
	}
	c, err := d.sql.Conn(ctx)
	if err != nil {
		return SQLResult{}, err
	}
	defer c.Close()

	var ro bool
	if !allowWrite {
		if _, err := c.ExecContext(ctx, "BEGIN TRANSACTION READ ONLY"); err != nil {
			return SQLResult{}, err
		}
		ro = true
	}
	defer func() {
		if !ro {
			return
		}
		if err := rollbackConn(c); err != nil {
			discardConn(c)
		}
	}()

	if err := inspectUserSQL(c, query, allowWrite); err != nil {
		return SQLResult{}, err
	}
	rows, err := c.QueryContext(ctx, query)
	if err != nil {
		return SQLResult{}, err
	}
	res, err := scanSQLResult(rows)
	closeErr := rows.Close()
	if err != nil {
		return SQLResult{}, err
	}
	if closeErr != nil {
		return SQLResult{}, closeErr
	}
	return res, rows.Err()
}

func rejectSQLText(query string) error {
	if strings.IndexByte(query, 0) >= 0 {
		return fmt.Errorf("query contains a NUL byte")
	}
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("empty query")
	}
	return nil
}

// inspectUserSQL uses native Prepare. Prepare must not execute the statement
// or any prefix. Statement type is the allow-list input.
func inspectUserSQL(c *sql.Conn, query string, allowWrite bool) error {
	return c.Raw(func(dc any) error {
		conn, ok := dc.(*duckdb.Conn)
		if !ok {
			return fmt.Errorf("unexpected connection type")
		}
		ps, err := conn.Prepare(query)
		if err != nil {
			return err
		}
		stmt, ok := ps.(*duckdb.Stmt)
		if !ok {
			_ = ps.Close()
			return fmt.Errorf("unexpected statement type")
		}
		kind, err := stmt.StatementType()
		closeErr := stmt.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return allowUserSQL(kind, allowWrite)
	})
}

func allowUserSQL(kind duckdb.StmtType, allowWrite bool) error {
	switch kind {
	case duckdb.STATEMENT_TYPE_SELECT, duckdb.STATEMENT_TYPE_EXPLAIN:
		return nil
	case duckdb.STATEMENT_TYPE_INSERT, duckdb.STATEMENT_TYPE_UPDATE, duckdb.STATEMENT_TYPE_DELETE,
		duckdb.STATEMENT_TYPE_CREATE, duckdb.STATEMENT_TYPE_ALTER, duckdb.STATEMENT_TYPE_DROP:
		if !allowWrite {
			return fmt.Errorf("write SQL requires --write")
		}
		return nil
	case duckdb.STATEMENT_TYPE_MULTI:
		return fmt.Errorf("multiple SQL statements are not allowed")
	default:
		return fmt.Errorf("SQL statement is not allowed")
	}
}

func rollbackConn(c *sql.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := c.ExecContext(ctx, "ROLLBACK")
	return err
}

func discardConn(c *sql.Conn) {
	_ = c.Raw(func(any) error { return driver.ErrBadConn })
}

func scanSQLResult(rows *sql.Rows) (SQLResult, error) {
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
