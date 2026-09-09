package query

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

const (
	oscClipboard = "\x1b]52;c;QUJD\x07"
	oscTitle     = "\x1b]2;evil-title\x07"
	csiRed       = "\x1b[31mred\x1b[0m"
	c1CSI        = "\u009b" + "31mC1"
	bidiRLO      = "\u202e"
	bidiLRI      = "\u2066"
)

func TestFormatHumanEscapesTerminalPayloads(t *testing.T) {
	body := "hello\nworld\tkeep" + oscTitle + "\x08" + "\r" + "\x07"
	env := Envelope{
		SchemaVersion: 1,
		LastSync:      csiRed,
		Phase:         "sync" + string(rune(0x9d)),
		LastError:     "gmail: " + oscClipboard,
		BodyCoverage:  BodyCoverage{WithBody: 1, Total: 1, SearchCovers: "metadata\r"},
		Message: &Message{
			ID:        "m1",
			ThreadID:  "t1",
			FromEmail: "alice@x.com",
			Subject:   "café 🎉 مرحبا " + bidiRLO + "\u200eexe\nrow\tcol",
			Body:      body,
		},
		Schema: &Schema{Tables: []store.TableSchema{{
			Name:    "messages" + bidiLRI,
			Columns: []store.ColumnSchema{{Name: "subject\nid", Type: "VARCHAR" + c1CSI}},
		}}},
		Checks: []Check{{Name: "api", OK: false, Detail: "err " + oscClipboard}},
	}

	out := FormatHuman(env)
	assertNoActiveTerminal(t, out)
	if !utf8.ValidString(out) {
		t.Fatal("human output is not valid UTF-8")
	}
	for _, keep := range []string{"café", "🎉", "مرحبا", "alice@x.com", "hello", "world", "keep", "QUJD", "evil-title", "red", "api"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("lost %q in %q", keep, out)
		}
	}
	if !strings.Contains(out, "\\") {
		t.Fatal("controls were stripped instead of shown")
	}
	if !strings.Contains(out, "hello\nworld\tkeep") {
		t.Fatalf("body lost intended LF/TAB: %q", out)
	}
	if strings.Count(out, "\nhello\nworld") != 1 {
		t.Fatalf("body not readable as its own lines: %q", out)
	}

	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var back Envelope
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.LastError != env.LastError || back.Message.Body != body || back.Message.Subject != env.Message.Subject {
		t.Fatal("JSON roundtrip changed stored values")
	}
	if back.Checks[0].Detail != env.Checks[0].Detail || back.Schema.Tables[0].Name != env.Schema.Tables[0].Name {
		t.Fatal("JSON roundtrip changed schema or check text")
	}
	if !strings.Contains(string(raw), "\\u001b") && !strings.Contains(string(raw), "\u001b") {
		t.Fatal("JSON lost the original ESC value")
	}
}

func TestFormatHumanSearchRowKeepsFormatterSeparators(t *testing.T) {
	env := Envelope{
		SchemaVersion: 1,
		BodyCoverage:  BodyCoverage{SearchCovers: "metadata"},
		Messages: []Message{{
			ID:        "m1",
			FromEmail: "alice@x.com",
			Subject:   "inv\noice\tpay" + csiRed,
		}},
	}
	out := FormatHuman(env)
	assertNoActiveTerminal(t, out)
	rows := dataRowsAfterBodies(t, out)
	if len(rows) != 1 {
		t.Fatalf("embedded newline/tab split the table: %#v", rows)
	}
	if strings.Count(rows[0], "\t") != 2 {
		t.Fatalf("formatter tabs changed: %q", rows[0])
	}
	if !strings.Contains(rows[0], "alice@x.com") || !strings.Contains(rows[0], "inv") || !strings.Contains(rows[0], "oice") {
		t.Fatalf("lost row text: %q", rows[0])
	}
}

func TestFormatHumanShowsInvalidUTF8(t *testing.T) {
	env := Envelope{LastError: "bad\xfftext"}
	out := FormatHuman(env)
	if !utf8.ValidString(out) {
		t.Fatalf("invalid UTF-8 left in %q", out)
	}
	if strings.Contains(out, "\xff") {
		t.Fatal("raw invalid byte left in output")
	}
	if !strings.Contains(out, "bad") || !strings.Contains(out, "text") {
		t.Fatalf("lost surrounding text: %q", out)
	}
	if !strings.Contains(out, "\\") {
		t.Fatal("invalid byte was not shown")
	}
}

func dataRowsAfterBodies(t *testing.T, out string) []string {
	t.Helper()
	var rows []string
	seen := false
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if strings.HasPrefix(line, "bodies ") {
			seen = true
			continue
		}
		if seen {
			rows = append(rows, line)
		}
	}
	if !seen {
		t.Fatal("missing bodies line")
	}
	return rows
}

func assertNoActiveTerminal(t *testing.T, s string) {
	t.Helper()
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
