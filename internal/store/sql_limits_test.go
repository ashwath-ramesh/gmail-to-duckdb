package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestQuerySQLStopsAtRowLimit(t *testing.T) {
	db := testDB(t)
	res, err := db.QuerySQL(context.Background(), "SELECT i FROM range(200000) t(i)", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Rows) != MaxSQLRows {
		t.Fatalf("truncated=%v rows=%d, want true and %d", res.Truncated, len(res.Rows), MaxSQLRows)
	}
	small, err := db.QuerySQL(context.Background(), "SELECT i FROM range(10) t(i)", false)
	if err != nil {
		t.Fatal(err)
	}
	if small.Truncated || len(small.Rows) != 10 {
		t.Fatalf("small result truncated=%v rows=%d", small.Truncated, len(small.Rows))
	}
}

func TestQuerySQLStopsAtByteLimit(t *testing.T) {
	db := testDB(t)
	// 200 rows of 1 MiB each is far over MaxSQLBytes but under MaxSQLRows.
	res, err := db.QuerySQL(context.Background(), "SELECT repeat('x', 1048576) AS s FROM range(200)", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("want truncated")
	}
	if n := len(res.Rows); n == 0 || n*(1<<20) > MaxSQLBytes {
		t.Fatalf("kept %d rows of 1 MiB, limit is %d bytes", n, MaxSQLBytes)
	}
}

func TestQuerySQLTimeout(t *testing.T) {
	db := testDB(t)
	old := sqlTimeout
	sqlTimeout = 200 * time.Millisecond
	t.Cleanup(func() { sqlTimeout = old })
	start := time.Now()
	_, err := db.QuerySQL(context.Background(), "SELECT sum(a.i * b.i) FROM range(100000000) a(i), range(1000) b(i)", false)
	if err == nil || !strings.Contains(err.Error(), "SQL stopped after") {
		t.Fatalf("err = %v, want the time limit error", err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("query ran %s after the time limit", d)
	}
	// The connection must still work after the interrupt.
	if _, err := db.QuerySQL(context.Background(), "SELECT 1", false); err != nil {
		t.Fatalf("after timeout: %v", err)
	}
}

func TestQuerySQLValuesAlwaysEncodeAsJSON(t *testing.T) {
	db := testDB(t)
	res, err := db.QuerySQL(context.Background(), `SELECT
		'nan'::DOUBLE AS nan,
		'inf'::DOUBLE AS inf,
		'-inf'::FLOAT AS ninf,
		[1.5::DOUBLE, 'nan'::DOUBLE] AS list,
		{'x': 'inf'::DOUBLE} AS st,
		MAP {'a': 'nan'::DOUBLE} AS m,
		1.50::DECIMAL(10,2) AS dec,
		'0b5d2a52-1c5e-4c1e-9d2b-3f0a6c7e8d91'::UUID AS u`, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got := string(b)
	for _, want := range []string{
		`"NaN","Infinity","-Infinity"`,
		`[1.5,"NaN"]`,
		`{"x":"Infinity"}`,
		`{"a":"NaN"}`,
		`1.5,`,
		`"0b5d2a52-1c5e-4c1e-9d2b-3f0a6c7e8d91"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("JSON %s\nmissing %s", got, want)
		}
	}
}
