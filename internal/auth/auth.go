package auth

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	gmailapi "google.golang.org/api/gmail/v1"
)

// DefaultOAuthPort is a fixed loopback port so a remote SSH tunnel can stay open.
const DefaultOAuthPort = 41807

func TokenPath(dbPath string) string {
	ext := filepath.Ext(dbPath)
	return strings.TrimSuffix(dbPath, ext) + ".token.json"
}

func HTTPClient(ctx context.Context, credPath, tokenPath string, oauthPort int) (*http.Client, error) {
	raw, err := os.ReadFile(credPath)
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
	_ = saveToken(p.path, tok)
	return tok, nil
}

func loadToken(path string) (*oauth2.Token, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, err
	}
	if !tok.Valid() && tok.RefreshToken == "" {
		return nil, fmt.Errorf("token expired")
	}
	return &tok, nil
}

func saveToken(path string, tok *oauth2.Token) error {
	b, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	return writeFile0600(path, b)
}

func writeFile0600(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func login(ctx context.Context, cfg *oauth2.Config, port int) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("oauth listen 127.0.0.1:%d: %w", port, err)
	}
	defer ln.Close()
	cfg = cloneConfig(cfg)
	cfg.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/", port)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("error") != "" {
			errCh <- fmt.Errorf("oauth: %s", r.URL.Query().Get("error"))
			http.Error(w, "auth failed", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("Signed in. You can close this tab."))
		codeCh <- code
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	authURL := cfg.AuthCodeURL("state", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	fmt.Fprintf(os.Stderr, "OAuth callback: http://127.0.0.1:%d/\n", port)
	fmt.Fprintf(os.Stderr, "If the browser is on another machine, open a tunnel first:\n")
	fmt.Fprintf(os.Stderr, "  ssh -L %d:127.0.0.1:%d USER@THIS_HOST\n", port, port)
	fmt.Fprintf(os.Stderr, "Or paste the redirect URL (or the code= value) here and press Enter.\n")
	fmt.Fprintf(os.Stderr, "Open this URL to sign in:\n%s\n", authURL)
	_ = openBrowser(authURL)

	loginCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go readPastedCode(loginCtx, codeCh, errCh)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-errCh:
		return nil, err
	case code := <-codeCh:
		return cfg.Exchange(ctx, code)
	}
}

func readPastedCode(ctx context.Context, codeCh chan<- string, errCh chan<- error) {
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return
	}
	code, err := CodeFromRedirect(sc.Text())
	if err != nil {
		select {
		case errCh <- err:
		case <-ctx.Done():
		}
		return
	}
	select {
	case codeCh <- code:
	case <-ctx.Done():
	}
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
	return exec.Command("xdg-open", url).Start()
}
