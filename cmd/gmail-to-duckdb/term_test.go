package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

const (
	cliOSC52  = "\x1b]52;c;QUJD\x07"
	cliOSCTit = "\x1b]2;evil-title\x07"
	cliCSI    = "\x1b[31mred\x1b[0m"
	cliC1     = "\u009b" + "31mC1"
	cliBidi   = "\u202e"
)

func seedHostileMail(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		dir = filepath.Join(dir, "db")
		if err := privfile.MkdirPrivate(dir); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "mail.duckdb")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.UpsertMessages(ctx, []store.Message{{
		ID: "m1", ThreadID: "t1", InternalDate: time.Unix(1, 0).UTC(),
		FromEmail: "alice@x.com",
		Subject:   "café 🎉 مرحبا שלום inv\noice\t" + cliBidi + "\u200e\u200f\u061c" + cliCSI,
		Snippet:   "pay" + cliC1,
		Body:      "hello\nworld\tkeep" + cliOSCTit + "\x08\r\x07",
		HasBody:   true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetState(ctx, store.StateLastSyncError, "gmail: "+cliOSC52); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func TestCLIHumanGetSearchSQLEscapeControls(t *testing.T) {
	dbPath := seedHostileMail(t)

	var status bytes.Buffer
	if err := run([]string{"gmail-to-duckdb", "status", "--db", dbPath}, strings.NewReader(""), &status, &status); err != nil {
		t.Fatal(err)
	}
	assertCLISafe(t, status.String())
	if !strings.Contains(status.String(), "QUJD") {
		t.Fatalf("status lost last_error text: %q", status.String())
	}

	var search bytes.Buffer
	if err := run([]string{"gmail-to-duckdb", "search", "--db", dbPath, "from:alice"}, strings.NewReader(""), &search, &search); err != nil {
		t.Fatal(err)
	}
	assertCLISafe(t, search.String())
	if !strings.Contains(search.String(), "café") || !strings.Contains(search.String(), "🎉") ||
		!strings.Contains(search.String(), "مرحبا") || !strings.Contains(search.String(), "שלום") {
		t.Fatalf("search lost unicode: %q", search.String())
	}
	if strings.Count(search.String(), "\nm1\t") != 1 && !strings.Contains(search.String(), "m1\t") {
		t.Fatalf("search row missing: %q", search.String())
	}
	row := ""
	for _, line := range strings.Split(strings.TrimSuffix(search.String(), "\n"), "\n") {
		if strings.HasPrefix(line, "m1\t") {
			row = line
		}
	}
	if row == "" || strings.Count(row, "\t") != 2 {
		t.Fatalf("search table split on embedded newline/tab: %q", search.String())
	}

	var get bytes.Buffer
	if err := run([]string{"gmail-to-duckdb", "get", "--db", dbPath, "--body", "m1"}, strings.NewReader(""), &get, &get); err != nil {
		t.Fatal(err)
	}
	assertCLISafe(t, get.String())
	if !strings.Contains(get.String(), "hello\nworld\tkeep") {
		t.Fatalf("get body lost intended LF/TAB: %q", get.String())
	}
	if !strings.Contains(get.String(), "evil-title") {
		t.Fatalf("get body lost title payload: %q", get.String())
	}

	var sqlOut bytes.Buffer
	q := `SELECT id, subject, row_to_json(messages) AS data FROM messages`
	if err := run([]string{"gmail-to-duckdb", "sql", "--db", dbPath, q}, strings.NewReader(""), &sqlOut, &sqlOut); err != nil {
		t.Fatal(err)
	}
	assertCLISafe(t, sqlOut.String())
	if !strings.Contains(sqlOut.String(), "m1") || !strings.Contains(sqlOut.String(), "café") {
		t.Fatalf("sql table lost values: %q", sqlOut.String())
	}
}

func TestCLIJSONAndStringRowsKeepOriginal(t *testing.T) {
	dbPath := seedHostileMail(t)
	var out bytes.Buffer
	if err := run([]string{"gmail-to-duckdb", "get", "--db", dbPath, "--json", "--body", "m1"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	var env query.Envelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Message == nil || !strings.Contains(env.Message.Subject, "\x1b") || !strings.Contains(env.Message.Body, "\x1b") {
		t.Fatalf("JSON get lost original controls: %#v", env.Message)
	}
	if env.Message.Body != "hello\nworld\tkeep"+cliOSCTit+"\x08\r\x07" {
		t.Fatalf("JSON body changed: %q", env.Message.Body)
	}

	out.Reset()
	if err := run([]string{"gmail-to-duckdb", "sql", "--db", dbPath, "--json", "SELECT subject FROM messages"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.SQL == nil || len(env.SQL.Rows) != 1 {
		t.Fatalf("sql json %#v", env.SQL)
	}
	cell := fmt.Sprint(env.SQL.Rows[0][0])
	if !strings.Contains(cell, "\x1b") || !strings.Contains(cell, "\n") {
		t.Fatalf("sql json cell was escaped: %q", cell)
	}
	rows := store.SQLResult{Rows: env.SQL.Rows}.StringRows()
	if !strings.Contains(rows[0][0], "\x1b") {
		t.Fatal("StringRows changed non-presentation data")
	}
}

func TestWriteSQLTableEscapesNestedAfterSprint(t *testing.T) {
	var buf bytes.Buffer
	err := writeSQLTable(&buf, &query.SQLPayload{
		Columns: []string{"data\ncol", "x\t" + cliCSI},
		Rows: [][]any{{
			map[string]any{"s": cliOSC52},
			[]any{"a\nb", cliC1},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	assertCLISafe(t, out)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("nested newline/tab split the table: %#v", lines)
	}
	if !strings.Contains(out, "QUJD") {
		t.Fatalf("lost nested payload: %q", out)
	}
}

func TestUsageLayoutNoArgsUnknownHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"gmail-to-duckdb"}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("no args must fail")
	}
	if strings.Contains(err.Error(), "Commands:") {
		t.Fatalf("no-args error embedded usage: %q", err.Error())
	}
	if stdout.Len() != 0 {
		t.Fatalf("no-args usage must go to stderr: %q", stdout.String())
	}
	assertUsageLayout(t, stderr.String())

	stdout.Reset()
	stderr.Reset()
	err = run([]string{"gmail-to-duckdb", "nope" + cliOSC52}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("unknown command must fail")
	}
	if strings.Contains(err.Error(), "Commands:") {
		t.Fatalf("unknown-command error embedded usage: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown-command error lost name: %v", err)
	}
	assertUsageLayout(t, stderr.String())
	var diag bytes.Buffer
	writeDiag(&diag, err.Error())
	assertCLISafe(t, diag.String())
	if strings.Contains(diag.String(), `\n  init`) {
		t.Fatalf("writeDiag flattened usage: %q", diag.String())
	}

	for _, args := range [][]string{
		{"gmail-to-duckdb", "help"},
		{"gmail-to-duckdb", "--help"},
		{"gmail-to-duckdb", "-h"},
	} {
		stdout.Reset()
		stderr.Reset()
		if err := run(args, strings.NewReader(""), &stdout, &stderr); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if stderr.Len() != 0 {
			t.Fatalf("%v wrote stderr: %q", args, stderr.String())
		}
		assertUsageLayout(t, stdout.String())
	}
}

func assertUsageLayout(t *testing.T, out string) {
	t.Helper()
	if !strings.Contains(out, "\n  init") || !strings.Contains(out, "\n  sync") || !strings.Contains(out, "\n  serve") {
		t.Fatalf("usage lost line layout: %q", out)
	}
	if strings.Contains(out, `\n  init`) {
		t.Fatalf("usage newlines were escaped: %q", out)
	}
}

func TestCmdInitEscapesPaths(t *testing.T) {
	cfgHome := filepath.Join(t.TempDir(), "cfg\u200e")
	dataHome := filepath.Join(t.TempDir(), "data\u061c")
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	src := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(src, []byte(`{"installed":{"client_id":"x","client_secret":"y"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(t.TempDir(), "mail\u200f.duckdb")
	var out bytes.Buffer
	if err := run([]string{"gmail-to-duckdb", "init", "--credentials", src, "--db", db}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	assertCLISafe(t, out.String())
	var wrote, creds, dbLine bool
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "wrote "):
			wrote = true
		case strings.HasPrefix(line, "credentials "):
			creds = true
		case strings.HasPrefix(line, "db "):
			dbLine = true
			if !strings.Contains(line, "mail") || !strings.Contains(line, "duckdb") {
				t.Fatalf("db path lost text: %q", line)
			}
		}
	}
	if !wrote || !creds || !dbLine {
		t.Fatalf("init path lines collapsed: %q", out.String())
	}
	if !strings.Contains(out.String(), "\nwrote ") || !strings.Contains(out.String(), "\ncredentials ") || !strings.Contains(out.String(), "\ndb ") {
		t.Fatalf("init path layout flattened: %q", out.String())
	}
}

func TestCLIDiagnosticPrintEscapesControls(t *testing.T) {
	dbPath := seedHostileMail(t)
	var out bytes.Buffer
	err := run([]string{"gmail-to-duckdb", "get", "--db", dbPath, "missing" + cliOSC52}, strings.NewReader(""), &out, &out)
	if err == nil {
		t.Fatal("expected missing-id error")
	}
	if !strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("error value should keep original text: %v", err)
	}
	var diag bytes.Buffer
	writeDiag(&diag, err.Error())
	assertCLISafe(t, diag.String())
	if !strings.Contains(diag.String(), "missing") {
		t.Fatalf("diagnostic lost text: %q", diag.String())
	}
}

func assertCLISafe(t *testing.T, s string) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatal("invalid UTF-8 in CLI output")
	}
	for _, r := range s {
		switch r {
		case 0x07, 0x08, 0x0d, 0x1b, 0x7f:
			t.Fatalf("raw control U+%04X in %q", r, s)
		default:
			if r < 0x20 && r != '\n' && r != '\t' {
				t.Fatalf("raw C0 U+%04X in %q", r, s)
			}
			if r >= 0x80 && r <= 0x9f {
				t.Fatalf("raw C1 U+%04X in %q", r, s)
			}
			if r == 0x061c || r == 0x200e || r == 0x200f || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
				t.Fatalf("raw bidi U+%04X in %q", r, s)
			}
		}
	}
}
