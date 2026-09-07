package web

import "testing"

func TestNewToken(t *testing.T) {
	a, err := NewToken()
	if err != nil || a == "" {
		t.Fatalf("%q %v", a, err)
	}
	b, err := NewToken()
	if err != nil || a == b {
		t.Fatalf("tokens should differ")
	}
}
