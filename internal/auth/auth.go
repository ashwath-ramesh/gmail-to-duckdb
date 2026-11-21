package auth

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/openurl"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
	"golang.org/x/oauth2"
)

// DefaultOAuthPort is a fixed loopback port so a remote SSH tunnel can stay open.
const DefaultOAuthPort = 41807

// Each HTTP request has a time limit, so that one hung request cannot stop
// sync forever. Gmail batch replies can be large, so they get more time.
const (
	tokenRequestTimeout = 30 * time.Second
	apiRequestTimeout   = 2 * time.Minute
)

var errNeedLogin = errors.New("token unusable")
var errInvalidGrant = errors.New("oauth refresh token rejected")

func TokenPath(dbPath string) string {
	ext := filepath.Ext(dbPath)
	return strings.TrimSuffix(dbPath, ext) + ".token.json"
}

// Google's OAuth endpoints, for a client file that does not name them.
const (
	googleAuthURL  = "https://accounts.google.com/o/oauth2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token"
)

// CheckDesktopClient checks that raw is a Google OAuth Desktop client file.
func CheckDesktopClient(raw []byte) error {
	_, err := desktopConfig(raw)
	return err
}

// desktopConfig reads a Google OAuth Desktop client file. The file names the
// OAuth endpoints, so they must be Google HTTPS hosts: otherwise a changed
// file could send the sign-in code and the refresh token to another server.
func desktopConfig(raw []byte) (*oauth2.Config, error) {
	var doc struct {
		Installed *struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
			AuthURI      string `json:"auth_uri"`
			TokenURI     string `json:"token_uri"`
		} `json:"installed"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("credentials JSON: %w", err)
	}
	in := doc.Installed
	if in == nil {
		return nil, fmt.Errorf("credentials must be a Desktop app client (key \"installed\")")
	}
	if in.ClientID == "" || in.ClientSecret == "" {
		return nil, fmt.Errorf("credentials must have client_id and client_secret")
	}
	cfg := &oauth2.Config{
		ClientID:     in.ClientID,
		ClientSecret: in.ClientSecret,
		Scopes:       []string{GmailReadonlyScope},
		Endpoint:     oauth2.Endpoint{AuthURL: googleAuthURL, TokenURL: googleTokenURL},
	}
	for _, f := range []struct {
		name, v string
		dst     *string
	}{{"auth_uri", in.AuthURI, &cfg.Endpoint.AuthURL}, {"token_uri", in.TokenURI, &cfg.Endpoint.TokenURL}} {
		if f.v == "" {
			continue
		}
		u, err := url.Parse(f.v)
		if err != nil || u.Scheme != "https" || u.User != nil || !googleOAuthHost(u.Host) {
			return nil, fmt.Errorf("credentials %s must be a Google https URL, got %q", f.name, f.v)
		}
		*f.dst = f.v
	}
	return cfg, nil
}

func googleOAuthHost(h string) bool {
	switch strings.ToLower(h) {
	case "accounts.google.com", "oauth2.googleapis.com", "www.googleapis.com":
		return true
	}
	return false
}

// GmailReadonlyScope is the only Google permission this tool asks for.
const GmailReadonlyScope = "https://www.googleapis.com/auth/gmail.readonly"

// HTTPClient returns a Gmail client. With no saved token, it signs in first;
// browser tells it to open the Google page (it always prints the link).
func HTTPClient(ctx context.Context, credPath, tokenPath string, oauthPort int, browser bool) (*http.Client, error) {
	raw, err := privfile.Read(credPath)
	if err != nil {
		return nil, fmt.Errorf("read credentials %s: %w", credPath, err)
	}
	cfg, err := desktopConfig(raw)
	if err != nil {
		return nil, err
	}
	if oauthPort <= 0 {
		oauthPort = DefaultOAuthPort
	}
	// oauth2 uses this client for the code exchange and for token refresh.
	ctx = context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: tokenRequestTimeout})
	tok, scope, err := loadToken(tokenPath)
	if err != nil {
		if !errors.Is(err, errNeedLogin) {
			return nil, err
		}
		tok, err = login(ctx, cfg, oauthPort, browser)
		if err != nil {
			return nil, err
		}
		scope = grantedScope(tok)
		if err := checkScope(scope); err != nil {
			return nil, err
		}
		if err := saveToken(tokenPath, tok, scope); err != nil {
			return nil, err
		}
	}
	src := &persistSource{src: cfg.TokenSource(ctx, tok), path: tokenPath, scope: scope}
	hc := oauth2.NewClient(ctx, src)
	hc.Timeout = apiRequestTimeout
	return hc, nil
}

type persistSource struct {
	src   oauth2.TokenSource
	path  string
	scope string
}

func (p *persistSource) Token() (*oauth2.Token, error) {
	tok, err := p.src.Token()
	if err != nil {
		return nil, wrapRefreshError(err)
	}
	if s := grantedScope(tok); s != "" {
		p.scope = s
	}
	if err := saveToken(p.path, tok, p.scope); err != nil {
		return nil, err
	}
	return tok, nil
}

// grantedScope is the space-separated scope list from Google's token reply.
func grantedScope(tok *oauth2.Token) string {
	s, _ := tok.Extra("scope").(string)
	return s
}

// checkScope rejects a sign-in where the user did not allow Gmail access.
// An empty scope means that Google did not say, so it is accepted.
func checkScope(scope string) error {
	if scope == "" || slices.Contains(strings.Fields(scope), GmailReadonlyScope) {
		return nil
	}
	return fmt.Errorf("Google did not give Gmail read access. Sign in again, and on the Google page keep the box that allows this app to read your email")
}

func wrapRefreshError(err error) error {
	if !isInvalidGrant(err) {
		return err
	}
	return fmt.Errorf("%w: stop serve, run gmail-to-duckdb logout, then start serve and sign in with the same account; do not delete the database or credentials", errInvalidGrant)
}

func isInvalidGrant(err error) bool {
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) {
		return false
	}
	if re.ErrorCode == "invalid_grant" {
		return true
	}
	var payload struct {
		Error string `json:"error"`
	}
	return json.Unmarshal(re.Body, &payload) == nil && payload.Error == "invalid_grant"
}

// storedToken is the token file: the oauth2 token plus the granted scope,
// which oauth2.Token does not keep.
type storedToken struct {
	oauth2.Token
	Scope string `json:"scope,omitempty"`
}

func loadToken(path string) (*oauth2.Token, string, error) {
	b, err := privfile.Read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", errNeedLogin
		}
		return nil, "", err
	}
	var st storedToken
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, "", errNeedLogin
	}
	if !st.Valid() && st.RefreshToken == "" {
		return nil, "", errNeedLogin
	}
	return &st.Token, st.Scope, nil
}

func saveToken(path string, tok *oauth2.Token, scope string) error {
	b, err := json.MarshalIndent(storedToken{Token: *tok, Scope: scope}, "", "  ")
	if err != nil {
		return err
	}
	return privfile.Write(path, b)
}

type loginIO struct {
	in   io.Reader
	out  io.Writer
	open func(string) error
}

func login(ctx context.Context, cfg *oauth2.Config, port int, browser bool) (*oauth2.Token, error) {
	seams := loginIO{in: os.Stdin, out: os.Stderr}
	if browser {
		seams.open = openurl.Open
	}
	return doLogin(ctx, cfg, port, seams)
}

type loginResult struct {
	code string
	err  error
}

func doLogin(ctx context.Context, cfg *oauth2.Config, port int, seams loginIO) (*oauth2.Token, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("oauth listen 127.0.0.1:%d: %w", port, err)
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return nil, fmt.Errorf("oauth listen: unexpected addr %T", ln.Addr())
	}
	cp := *cfg
	cfg = &cp
	cfg.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/", addr.Port)

	state, err := randomState()
	if err != nil {
		return nil, err
	}
	verifier := oauth2.GenerateVerifier()

	results := make(chan loginResult, 1)
	var finish sync.Once
	complete := func(code string, err error) bool {
		sent := false
		finish.Do(func() {
			results <- loginResult{code: code, err: err}
			sent = true
		})
		return sent
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handleCallback(w, r, addr, state, complete)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer shutdownHTTP(srv)

	authURL := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce, oauth2.S256ChallengeOption(verifier))
	out := seams.out
	if out == nil {
		out = os.Stderr
	}
	fmt.Fprintf(out, "OAuth callback: %s\n", cfg.RedirectURL)
	fmt.Fprintf(out, "If the browser is on another machine, open a tunnel first:\n")
	fmt.Fprintf(out, "  ssh -L %d:127.0.0.1:%d USER@THIS_HOST\n", addr.Port, addr.Port)
	fmt.Fprintf(out, "Or paste the redirect URL (or the code= value) here and press Enter.\n")
	fmt.Fprintf(out, "Open this URL to sign in:\n%s\n", authURL)
	open := seams.open
	if open == nil {
		open = func(string) error { return nil }
	}
	_ = open(authURL)

	loginCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go readPastedLogin(loginCtx, seams.in, out, cfg.RedirectURL, state, complete)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-results:
		if res.err != nil {
			return nil, res.err
		}
		return cfg.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
	}
}

func shutdownHTTP(srv *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
	}
}

func handleCallback(w http.ResponseWriter, r *http.Request, addr *net.TCPAddr, state string, complete func(string, error) bool) {
	if r.Method != http.MethodGet || !callbackPathOK(r.URL.Path) || r.Host != addr.String() {
		http.Error(w, "invalid callback", http.StatusBadRequest)
		return
	}
	code, provErr, ok := parseOAuthCallback(r.URL.RawQuery, state, true)
	if !ok {
		http.Error(w, "invalid callback", http.StatusBadRequest)
		return
	}
	if provErr != nil {
		if !complete("", provErr) {
			http.Error(w, "invalid state", http.StatusBadRequest)
			return
		}
		http.Error(w, "auth failed", http.StatusBadRequest)
		return
	}
	if !complete(code, nil) {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}
	_, _ = w.Write([]byte("Signed in. You can close this tab."))
}

func readPastedLogin(ctx context.Context, in io.Reader, out io.Writer, redirect, state string, complete func(string, error) bool) {
	if in == nil {
		return
	}
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		if ctx.Err() != nil {
			return
		}
		code, err, ok := parsePasted(sc.Text(), redirect, state)
		if !ok {
			fmt.Fprintln(out, "Paste rejected. Use the callback URL with matching state, or the raw code.")
			continue
		}
		complete(code, err)
		return
	}
}

func parsePasted(s, redirect, state string) (string, error, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil, false
	}
	if isRedirectShape(s) {
		return parsePastedURL(s, redirect, state)
	}
	if strings.HasPrefix(s, "?") {
		return parsePastedQuery(strings.TrimPrefix(s, "?"), state)
	}
	if oauthQueryKeys(s) {
		return parsePastedQuery(s, state)
	}
	return s, nil, true
}

func oauthQueryKeys(s string) bool {
	q, _ := url.ParseQuery(s)
	_, hasCode := q["code"]
	_, hasState := q["state"]
	_, hasError := q["error"]
	return hasCode || hasState || hasError
}

func isRedirectShape(s string) bool {
	if strings.Contains(s, "://") {
		return true
	}
	low := strings.ToLower(s)
	return strings.HasPrefix(low, "http")
}

func parsePastedURL(s, redirect, state string) (string, error, bool) {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawFragment != "" {
		return "", nil, false
	}
	want, err := url.Parse(redirect)
	if err != nil {
		return "", nil, false
	}
	if u.Scheme != want.Scheme || !strings.EqualFold(u.Host, want.Host) || !callbackPathOK(u.Path) || !callbackPathOK(want.Path) {
		return "", nil, false
	}
	return parseOAuthCallback(u.RawQuery, state, true)
}

func parsePastedQuery(s, state string) (string, error, bool) {
	return parseOAuthCallback(s, state, false)
}

func parseOAuthCallback(rawQuery, expectedState string, requireState bool) (string, error, bool) {
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", nil, false
	}
	states := q["state"]
	switch len(states) {
	case 0:
		if requireState {
			return "", nil, false
		}
	case 1:
		if !stateEq(states[0], expectedState) {
			return "", nil, false
		}
	default:
		return "", nil, false
	}
	codes, errs := q["code"], q["error"]
	if len(codes) > 1 || len(errs) > 1 || (len(codes) == 1 && len(errs) == 1) {
		return "", nil, false
	}
	if len(errs) == 1 {
		if errs[0] == "" || len(states) != 1 {
			return "", nil, false
		}
		return "", fmt.Errorf("oauth: %s", errs[0]), true
	}
	if len(codes) != 1 || codes[0] == "" {
		return "", nil, false
	}
	return codes[0], nil, true
}

func randomState() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("oauth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func stateEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func callbackPathOK(p string) bool {
	return p == "/" || p == ""
}
