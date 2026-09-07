package htmlutil

import (
	"strings"
	"testing"
)

func TestLooksLikeHTML(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"plain text", false},
		{"price < 10", false},
		{"<3 love", false},
		{"  <p>Hi</p>  ", true},
		{"<html xmlns=\"http://www.w3.org/1999/xhtml\"><body>x</body></html>", true},
		{"<!DOCTYPE html><html><body>x</body></html>", true},
		{"  <HTML><BODY>x</BODY></HTML>", true},
		{"<div>Greetings<br></div>", true},
	}
	for _, c := range cases {
		if got := LooksLikeHTML(c.in); got != c.want {
			t.Fatalf("LooksLikeHTML(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestSanitizeStripsUnsafe(t *testing.T) {
	out := Sanitize(`<p>Hi<script>alert(1)</script></p><img src="x" onerror="alert(1)"><a href="javascript:alert(1)">x</a><iframe src="https://evil.example"></iframe>`)
	low := strings.ToLower(out)
	if strings.Contains(low, "script") || strings.Contains(low, "onerror") || strings.Contains(low, "javascript:") || strings.Contains(low, "iframe") {
		t.Fatalf("unsafe left in %q", out)
	}
	if !strings.Contains(out, "Hi") {
		t.Fatalf("lost text: %q", out)
	}
}

func TestSanitizeKeepsEmailMarkup(t *testing.T) {
	in := `<table role="presentation" bgcolor="#252F3D" width="100%" style="max-width: 600px;"><tr><td align="center"><a href="https://aws.amazon.com">AWS</a><img src="https://example.com/logo.png" alt="logo" width="75"></td></tr></table>`
	out := Sanitize(in)
	for _, want := range []string{"<table", "https://aws.amazon.com", "AWS"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}

func TestSanitizeStripsRemoteImages(t *testing.T) {
	out := Sanitize(`<p>Hi</p><img src="https://track.example/pixel.gif" alt="x"><img src="//cdn.example/a.png"><img src="https://example.com/logo.png">`)
	low := strings.ToLower(out)
	if strings.Contains(low, "track.example") || strings.Contains(low, "cdn.example") || strings.Contains(low, "example.com/logo") {
		t.Fatalf("remote image left in %q", out)
	}
	if !strings.Contains(out, "Hi") {
		t.Fatalf("lost text: %q", out)
	}
}
