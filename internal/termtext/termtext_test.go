package termtext

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSingleLineNeutralizesOSCAndCSI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		keep string
	}{
		{"osc52", "clip\x1b]52;c;QUJD\x07end", "QUJD"},
		{"osc_title", "t\x1b]2;evil-title\x07x", "evil-title"},
		{"csi", "a\x1b[31mred\x1b[0mz", "red"},
		{"c1", "x\u009b31mC1y", "C1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := SingleLine(tc.in)
			assertInert(t, out, false, false)
			if !strings.Contains(out, tc.keep) {
				t.Fatalf("lost %q in %q", tc.keep, out)
			}
			if !strings.Contains(out, "\\") {
				t.Fatal("sequence was stripped, not shown")
			}
		})
	}
}

func TestSingleLineEscapesRowBreaksAndEdits(t *testing.T) {
	out := SingleLine("a\nb\tc\rd\x08e")
	assertInert(t, out, false, false)
	if strings.ContainsAny(out, "\n\t\r\x08") {
		t.Fatalf("row-break controls left raw: %q", out)
	}
	for _, keep := range []string{"a", "b", "c", "d", "e"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("lost %q in %q", keep, out)
		}
	}
}

func TestSingleLineKeepsPrintableUnicode(t *testing.T) {
	in := "café 🎉"
	if SingleLine(in) != in {
		t.Fatalf("changed %q to %q", in, SingleLine(in))
	}
}

func TestSingleLineShowsInvalidUTF8(t *testing.T) {
	out := SingleLine("bad\xfftext")
	if !utf8.ValidString(out) {
		t.Fatalf("invalid UTF-8 left in %q", out)
	}
	if strings.Contains(out, "\xff") {
		t.Fatal("raw invalid byte left")
	}
	if !strings.Contains(out, "bad") || !strings.Contains(out, "text") || !strings.Contains(out, "\\") {
		t.Fatalf("invalid byte not shown: %q", out)
	}
}

func TestSingleLineEscapesBidiOverrides(t *testing.T) {
	in := "file" + "\u202e" + "exe" + "\u2066" + "x" + "\u061c" + "y" + "\u200e" + "z" + "\u200f"
	out := SingleLine(in)
	assertInert(t, out, false, false)
	for _, r := range []rune{0x202e, 0x2066, 0x061c, 0x200e, 0x200f} {
		if strings.ContainsRune(out, r) {
			t.Fatalf("raw bidi U+%04X left in %q", r, out)
		}
	}
	if !strings.Contains(out, "file") || !strings.Contains(out, "exe") {
		t.Fatalf("lost text: %q", out)
	}
	if !strings.Contains(out, "\\") {
		t.Fatal("bidi mark was stripped, not shown")
	}
}

func TestSingleLineKeepsArabicHebrew(t *testing.T) {
	in := "مرحبا שלום"
	if SingleLine(in) != in {
		t.Fatalf("changed %q to %q", in, SingleLine(in))
	}
}

func TestMultilineKeepsReadableBody(t *testing.T) {
	in := "hello\nworld\tkeep\x1b[31m\x08\r\x07"
	out := Multiline(in)
	assertInert(t, out, true, true)
	if !strings.Contains(out, "hello\nworld\tkeep") {
		t.Fatalf("lost intended LF/TAB: %q", out)
	}
	if strings.ContainsRune(out, 0x1b) || strings.ContainsRune(out, 0x08) || strings.ContainsRune(out, 0x0d) || strings.ContainsRune(out, 0x07) {
		t.Fatalf("active controls left in body: %q", out)
	}
	if !strings.Contains(out, "\\") {
		t.Fatal("body controls were stripped, not shown")
	}
}

func assertInert(t *testing.T, s string, allowLF, allowTab bool) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatal("invalid UTF-8")
	}
	for _, r := range s {
		switch r {
		case '\n':
			if !allowLF {
				t.Fatalf("raw LF in %q", s)
			}
		case '\t':
			if !allowTab {
				t.Fatalf("raw TAB in %q", s)
			}
		case 0x07, 0x08, 0x0d, 0x1b, 0x7f:
			t.Fatalf("raw control U+%04X in %q", r, s)
		default:
			if r < 0x20 {
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
