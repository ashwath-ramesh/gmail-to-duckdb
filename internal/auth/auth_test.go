package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestTokenPath(t *testing.T) {
	if got := TokenPath("mail.duckdb"); got != "mail.token.json" {
		t.Fatalf("got %s", got)
	}
	if got := TokenPath("/tmp/data/box.duckdb"); got != "/tmp/data/box.token.json" {
		t.Fatalf("got %s", got)
	}
}

func TestWriteTokenMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mail.token.json")
	if err := saveToken(path, &oauth2.Token{AccessToken: "x", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	assertPrivateFile(t, path)
}

func TestSaveTokenReplacesPermissiveFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mail.token.json")
	old := []byte(`{"access_token":"old"}`)
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makePermissive(path); err != nil {
		t.Fatal(err)
	}
	if err := saveToken(path, &oauth2.Token{AccessToken: "new", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	assertPrivateFile(t, path)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"old"`) || !strings.Contains(string(b), `"new"`) {
		t.Fatalf("content %s", b)
	}
}

func TestPersistSourceWrapsInvalidGrant(t *testing.T) {
	secret := "refresh-secret-value"
	body := []byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked.","access_token":"` + secret + `"}`)
	src := &persistSource{
		src: errTokenSource{err: &oauth2.RetrieveError{
			ErrorCode:        "invalid_grant",
			ErrorDescription: "Token has been expired or revoked.",
			Body:             body,
		}},
		path: filepath.Join(t.TempDir(), "mail.token.json"),
	}
	tok, err := src.Token()
	if err == nil {
		t.Fatal("expected invalid_grant wrap")
	}
	if tok != nil {
		t.Fatal("token on refresh error")
	}
	if !errors.Is(err, errInvalidGrant) {
		t.Fatalf("want errInvalidGrant, got %v", err)
	}
	if errors.Is(err, errNeedLogin) {
		t.Fatal("must not start login")
	}
	msg := err.Error()
	for _, need := range []string{"stop serve", "token file", "same account", "do not delete the database"} {
		if !strings.Contains(strings.ToLower(msg), need) {
			t.Fatalf("missing %q in %q", need, msg)
		}
	}
	for _, leak := range []string{secret, "access_token", string(body), "Token has been expired or revoked.", "Response:"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("leaked %q in %q", leak, msg)
		}
	}
}

func TestPersistSourceWrapsInvalidGrantFromBody(t *testing.T) {
	body := []byte(`{"error":"invalid_grant","refresh_token":"hidden-refresh"}`)
	src := &persistSource{
		src: errTokenSource{err: &oauth2.RetrieveError{
			Response: &http.Response{Status: "400 Bad Request"},
			Body:     body,
		}},
		path: filepath.Join(t.TempDir(), "mail.token.json"),
	}
	_, err := src.Token()
	if !errors.Is(err, errInvalidGrant) {
		t.Fatalf("want wrap from body, got %v", err)
	}
	if strings.Contains(err.Error(), "hidden-refresh") || strings.Contains(err.Error(), string(body)) {
		t.Fatalf("leaked body: %v", err)
	}
}

func TestPersistSourceLeavesOtherRefreshErrors(t *testing.T) {
	body := []byte(`{"error":"invalid_client","error_description":"keep"}`)
	orig := &oauth2.RetrieveError{ErrorCode: "invalid_client", Body: body}
	src := &persistSource{
		src:  errTokenSource{err: orig},
		path: filepath.Join(t.TempDir(), "mail.token.json"),
	}
	_, err := src.Token()
	if errors.Is(err, errInvalidGrant) {
		t.Fatal("wrapped non-grant")
	}
	if !errors.Is(err, orig) {
		t.Fatalf("lost original: %v", err)
	}
}

func TestPersistSourceReportsSaveError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mail.token.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	src := &persistSource{
		src: stubTokenSource{tok: &oauth2.Token{
			AccessToken:  "access-secret-value",
			RefreshToken: "refresh-secret-value",
		}},
		path: path,
	}
	tok, err := src.Token()
	if err == nil {
		t.Fatal("expected save error")
	}
	if tok != nil {
		t.Fatal("token on save error")
	}
	if strings.Contains(err.Error(), "access-secret-value") || strings.Contains(err.Error(), "refresh-secret-value") {
		t.Fatalf("error leaked secret: %v", err)
	}
}

func TestSaveTokenRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "other.json")
	path := filepath.Join(dir, "mail.token.json")
	keep := []byte(`{"access_token":"keep"}`)
	if err := os.WriteFile(target, keep, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		skipIfNoSymlink(t, err)
	}
	if err := saveToken(path, &oauth2.Token{AccessToken: "new", RefreshToken: "r"}); err == nil {
		t.Fatal("expected symlink reject")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(keep) {
		t.Fatalf("target changed: %s", got)
	}
}

func TestLoadTokenRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "other.json")
	path := filepath.Join(dir, "mail.token.json")
	if err := os.WriteFile(target, []byte(`{"refresh_token":"r"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		skipIfNoSymlink(t, err)
	}
	if _, err := loadToken(path); err == nil {
		t.Fatal("expected symlink reject")
	}
}

func TestLoadTokenNeedLogin(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.token.json")
	if _, err := loadToken(missing); !errors.Is(err, errNeedLogin) {
		t.Fatalf("missing: %v", err)
	}
	path := filepath.Join(t.TempDir(), "mail.token.json")
	if err := saveToken(path, &oauth2.Token{}); err != nil {
		t.Fatal(err)
	}
	if _, err := loadToken(path); !errors.Is(err, errNeedLogin) {
		t.Fatalf("expired: %v", err)
	}
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadToken(path); !errors.Is(err, errNeedLogin) {
		t.Fatalf("invalid: %v", err)
	}
}

func TestLoadTokenPrivacyError(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "other.json")
	path := filepath.Join(dir, "mail.token.json")
	if err := os.WriteFile(target, []byte(`{"refresh_token":"r"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		skipIfNoSymlink(t, err)
	}
	_, err := loadToken(path)
	if err == nil {
		t.Fatal("expected privacy error")
	}
	if errors.Is(err, errNeedLogin) {
		t.Fatal("privacy error masked as login")
	}
}

func TestHTTPClientRejectsPrivateTokenError(t *testing.T) {
	dir := t.TempDir()
	cred := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(cred, []byte(desktopCreds), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "other.json")
	if err := os.WriteFile(target, []byte(`{"refresh_token":"r"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tokPath := filepath.Join(dir, "mail.token.json")
	if err := os.Symlink(target, tokPath); err != nil {
		skipIfNoSymlink(t, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := HTTPClient(ctx, cred, tokPath, freePort(t))
	if err == nil {
		t.Fatal("expected privacy error")
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Fatal("started login")
	}
	if strings.Contains(err.Error(), "oauth listen") {
		t.Fatalf("started login: %v", err)
	}
}

func TestHTTPClientReauthsMissingToken(t *testing.T) {
	dir := t.TempDir()
	cred := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(cred, []byte(desktopCreds), 0o600); err != nil {
		t.Fatal(err)
	}
	port := heldPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := HTTPClient(ctx, cred, filepath.Join(dir, "missing.token.json"), port)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled login without listen, got %v", err)
	}
}

func TestHTTPClientReauthsExpiredToken(t *testing.T) {
	dir := t.TempDir()
	cred := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(cred, []byte(desktopCreds), 0o600); err != nil {
		t.Fatal(err)
	}
	tokPath := filepath.Join(dir, "mail.token.json")
	if err := saveToken(tokPath, &oauth2.Token{AccessToken: "x", Expiry: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	port := heldPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := HTTPClient(ctx, cred, tokPath, port)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled login without listen, got %v", err)
	}
}

func TestHTTPClientTightensCopiedCredentials(t *testing.T) {
	dir := t.TempDir()
	cred := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(cred, []byte(desktopCreds), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cred, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makePermissive(cred); err != nil {
		t.Fatal(err)
	}
	tokPath := filepath.Join(dir, "mail.token.json")
	if err := saveToken(tokPath, &oauth2.Token{RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	if _, err := HTTPClient(context.Background(), cred, tokPath, 41807); err != nil {
		t.Fatal(err)
	}
	assertPrivateFile(t, cred)
}

func TestFailedTokenReplaceLeavesPriorData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mail.token.json")
	if err := saveToken(path, &oauth2.Token{AccessToken: "old", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := makeDirNotCreatable(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restoreDirCreatable(dir) })
	err := saveToken(path, &oauth2.Token{AccessToken: "new", RefreshToken: "r"})
	if err == nil {
		t.Fatal("expected replace failure")
	}
	if err := restoreDirCreatable(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tok oauth2.Token
	if err := json.Unmarshal(b, &tok); err != nil {
		t.Fatalf("prior json lost: %v", err)
	}
	if tok.AccessToken != "old" {
		t.Fatalf("prior data lost: %+v", tok)
	}
	assertNoTempFiles(t, dir)
}

type stubTokenSource struct {
	tok *oauth2.Token
}

func (s stubTokenSource) Token() (*oauth2.Token, error) {
	return s.tok, nil
}

type errTokenSource struct {
	err error
}

func (s errTokenSource) Token() (*oauth2.Token, error) {
	return nil, s.err
}

const desktopCreds = `{"installed":{"client_id":"x.apps.googleusercontent.com","client_secret":"s","redirect_uris":["http://localhost"],"auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"https://oauth2.googleapis.com/token"}}`

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func heldPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}
