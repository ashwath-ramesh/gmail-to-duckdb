package web

import (
	"net/http"
	"testing"
)

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

func TestAPITokenHeaderAndCookie(t *testing.T) {
	s, _ := testServer(t)
	s.Token = "secret"
	h := s.Handler()
	host := "127.0.0.1:8080"

	cases := []struct {
		name   string
		header string
		cookie string
		setHdr bool
		setCk  bool
		want   int
	}{
		{name: "good header", header: "secret", setHdr: true, want: 200},
		{name: "good cookie", cookie: "secret", setCk: true, want: 200},
		{name: "bad header", header: "secrez", setHdr: true, want: 401},
		{name: "bad cookie", cookie: "secrez", setCk: true, want: 401},
		{name: "prefix header", header: "secre", setHdr: true, want: 401},
		{name: "prefix cookie", cookie: "secre", setCk: true, want: 401},
		{name: "longer header", header: "secretX", setHdr: true, want: 401},
		{name: "length mismatch header", header: "x", setHdr: true, want: 401},
		{name: "length mismatch cookie", cookie: "toolongtoken", setCk: true, want: 401},
		{name: "bad header good cookie fallback", header: "wrong", cookie: "secret", setHdr: true, setCk: true, want: 200},
		{name: "good header bad cookie", header: "secret", cookie: "wrong", setHdr: true, setCk: true, want: 200},
		{name: "bad header bad cookie", header: "wrong", cookie: "wrong", setHdr: true, setCk: true, want: 401},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hdr := http.Header{}
			if c.setHdr {
				hdr.Set("X-Token", c.header)
			}
			if c.setCk {
				hdr.Set("Cookie", "session="+c.cookie)
			}
			w := doReq(t, h, http.MethodGet, "/api/status", host, hdr, nil)
			if w.Code != c.want {
				t.Fatalf("code %d want %d body %s", w.Code, c.want, w.Body.String())
			}
		})
	}
}
