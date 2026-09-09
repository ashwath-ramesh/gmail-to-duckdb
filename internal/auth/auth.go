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
	"strings"
	"sync"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/openurl"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	gmailapi "google.golang.org/api/gmail/v1"
)

// DefaultOAuthPort is a fixed loopback port so a remote SSH tunnel can stay open.
const DefaultOAuthPort = 41807

var errNeedLogin = errors.New("token unusable")

func TokenPath(dbPath string) string {
	ext := filepath.Ext(dbPath)
	return strings.TrimSuffix(dbPath, ext) + ".token.json"
}

func HTTPClient(ctx context.Context, credPath, tokenPath string, oauthPort int) (*http.Client, error) {
	raw, err := privfile.Read(credPath)
	if err != nil {
		return nil, fmt.Errorf("read credentials %s: %w", credPath, err)
	}
	cfg, err := google.ConfigFromJSON(raw, gmailapi.GmailReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	if oauthPort <= 0 {
		oauthPort = DefaultOAuthPort
	}
	tok, err := loadToken(tokenPath)
	if err != nil {
		if !errors.Is(err, errNeedLogin) {
			return nil, err
		}
		tok, err = login(ctx, cfg, oauthPort)
		if err != nil {
			return nil, err
		}
		if err := saveToken(tokenPath, tok); err != nil {
			return nil, err
		}
	}
	src := &persistSource{src: cfg.TokenSource(ctx, tok), path: tokenPath}
	return oauth2.NewClient(ctx, src), nil
}

type persistSource struct {
	src  oauth2.TokenSource
	path string
}

func (p *persistSource) Token() (*oauth2.Token, error) {
	tok, err := p.src.Token()
	if err != nil {
		return nil, err
	}
	if err := saveToken(p.path, tok); err != nil {
		return nil, err
	}
	return tok, nil
}

func loadToken(path string) (*oauth2.Token, error) {
	b, err := privfile.Read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errNeedLogin
		}
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, errNeedLogin
	}
	if !tok.Valid() && tok.RefreshToken == "" {
		return nil, errNeedLogin
	}
	return &tok, nil
}

func saveToken(path string, tok *oauth2.Token) error {
	b, err := json.MarshalIndent(tok, "", "  ")
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

func login(ctx context.Context, cfg *oauth2.Config, port int) (*oauth2.Token, error) {
	return doLogin(ctx, cfg, port, loginIO{in: os.Stdin, out: os.Stderr, open: openBrowser})
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
	cfg = cloneConfig(cfg)
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

func CodeFromRedirect(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("empty redirect")
	}
	if strings.Contains(s, "://") || strings.HasPrefix(s, "http") {
		u, err := url.Parse(s)
		if err != nil {
			return "", err
		}
		code := u.Query().Get("code")
		if code == "" {
			return "", fmt.Errorf("redirect has no code")
		}
		return code, nil
	}
	if i := strings.Index(s, "code="); i >= 0 {
		return CodeFromRedirect("http://localhost/?" + s[i:])
	}
	return s, nil
}

func cloneConfig(cfg *oauth2.Config) *oauth2.Config {
	cp := *cfg
	return &cp
}

func openBrowser(url string) error {
	return openurl.Open(url)
}
