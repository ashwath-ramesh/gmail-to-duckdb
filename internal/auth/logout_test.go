package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/oauth2"
)

func savedToken(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mail.token.json")
	if err := saveToken(p, &oauth2.Token{AccessToken: "acc", RefreshToken: "ref"}, GmailReadonlyScope); err != nil {
		t.Fatal(err)
	}
	return p
}

func revokeServer(t *testing.T, code int, body string, got *url.Values) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.RawQuery != "" {
			t.Errorf("revoke request %s %s: the token must be in the body", r.Method, r.URL)
		}
		b, _ := io.ReadAll(r.Body)
		v, _ := url.ParseQuery(string(b))
		*got = v
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestLogoutRevokesRefreshTokenAndRemovesFile(t *testing.T) {
	p := savedToken(t)
	var got url.Values
	ts := revokeServer(t, 200, "{}", &got)
	revoked, err := logout(context.Background(), p, ts.URL, ts.Client())
	if err != nil || !revoked {
		t.Fatalf("revoked=%v err=%v", revoked, err)
	}
	if got.Get("token") != "ref" {
		t.Fatalf("revoked token %q, want the refresh token", got.Get("token"))
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("token file still there: %v", err)
	}
}

func TestLogoutAlreadyInvalidStillRemovesFile(t *testing.T) {
	p := savedToken(t)
	var got url.Values
	ts := revokeServer(t, 400, `{"error":"invalid_token","error_description":"Token expired or revoked"}`, &got)
	revoked, err := logout(context.Background(), p, ts.URL, ts.Client())
	if err != nil || revoked {
		t.Fatalf("revoked=%v err=%v, want false and nil", revoked, err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("token file still there: %v", err)
	}
}

func TestLogoutKeepsFileWhenGoogleFails(t *testing.T) {
	p := savedToken(t)
	var got url.Values
	ts := revokeServer(t, 503, `{"error":"backend_error"}`, &got)
	if _, err := logout(context.Background(), p, ts.URL, ts.Client()); err == nil {
		t.Fatal("want an error")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("token file removed after a failed revoke: %v", err)
	}
	ts.Close()
	if _, err := logout(context.Background(), p, ts.URL, ts.Client()); err == nil {
		t.Fatal("want a network error")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("token file removed after a network error: %v", err)
	}
}

func TestLogoutWithoutTokenFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mail.token.json")
	if _, err := logout(context.Background(), p, "http://127.0.0.1:1", http.DefaultClient); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("err = %v", err)
	}
}
