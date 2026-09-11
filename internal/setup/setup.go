package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/auth"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/config"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/web"
	gmailapi "google.golang.org/api/gmail/v1"
)

const SetupSteps = `Create a Google Cloud project.
Enable the Gmail API.
Configure the OAuth consent screen.
Use Internal consent only when the project is associated with an organization and only users in that organization sign in.
Otherwise use External consent.
If the app is External and in Testing, add yourself as a test user.
Create OAuth 2.0 credentials for a Desktop app.
The app requests the gmail.readonly scope.
Use your own OAuth client. Do not copy another person's credentials.json.
External apps in Testing expire refresh tokens after seven days.
Google Workspace admins can block or restrict access.
See https://support.google.com/cloud/answer/15549945?hl=en
Save the JSON file, then run:
  gmail-to-duckdb init --credentials PATH
`

func Init(credSrc, dbPath string) (config.Config, error) {
	if credSrc == "" {
		if _, err := os.Stat(config.DefaultCredentials); err == nil {
			credSrc = config.DefaultCredentials
		}
	}
	if credSrc == "" {
		return config.Config{}, fmt.Errorf("pass --credentials PATH to your OAuth Desktop client JSON")
	}
	raw, err := os.ReadFile(credSrc)
	if err != nil {
		return config.Config{}, err
	}
	if err := checkDesktopCreds(raw); err != nil {
		return config.Config{}, err
	}
	if err := privfile.MkdirPrivate(config.Dir()); err != nil {
		return config.Config{}, err
	}
	if err := privfile.HardenDir(config.Dir()); err != nil {
		return config.Config{}, err
	}
	if err := privfile.MkdirPrivate(config.DataDir()); err != nil {
		return config.Config{}, err
	}
	if err := privfile.HardenDir(config.DataDir()); err != nil {
		return config.Config{}, err
	}
	dest := filepath.Join(config.Dir(), "credentials.json")
	if err := privfile.Write(dest, raw); err != nil {
		return config.Config{}, err
	}
	if dbPath == "" {
		dbPath = filepath.Join(config.DataDir(), "mail.duckdb")
	}
	cfg := config.Config{
		DB:          dbPath,
		Credentials: dest,
		OAuthPort:   auth.DefaultOAuthPort,
		Port:        config.DefaultPort,
	}
	if err := config.Write(cfg); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func Doctor(ctx context.Context, cfg config.Config) query.Envelope {
	env := query.Envelope{SchemaVersion: store.SchemaVersion}
	env.Checks = append(env.Checks, checkCredentials(cfg.Credentials))
	env.Checks = append(env.Checks, checkToken(auth.TokenPath(cfg.DB)))
	if info, err := web.ReadServeFile(cfg.DB); err == nil {
		if u, err := url.Parse(info.URL); err == nil && u.Port() != "" {
			cfg.Port = u.Port()
		}
		if c, err := web.Dial(cfg.DB); err == nil {
			defer c.Close()
			if st, err := c.Status(ctx); err == nil {
				env.LastSync = st.LastSync
				env.BodyCoverage = st.BodyCoverage
				env.SchemaVersion = st.SchemaVersion
				env.LastError = st.LastError
				env.Checks = append(env.Checks, query.Check{Name: "database", OK: true, Detail: "owned by serve"})
				env.Checks = append(env.Checks, query.Check{Name: "fts", OK: true, Detail: ftsDetail(st.FTS)})
			} else {
				env.Checks = append(env.Checks, query.Check{Name: "database", OK: false, Detail: err.Error()})
				env.Checks = append(env.Checks, query.Check{Name: "fts", OK: false, Detail: "serve status failed"})
			}
		} else {
			env.Checks = append(env.Checks, query.Check{Name: "database", OK: false, Detail: "serve is marked running but not reachable: " + err.Error()})
			env.Checks = append(env.Checks, query.Check{Name: "fts", OK: false, Detail: "database owned by unreachable serve"})
		}
	} else if !os.IsNotExist(err) {
		env.Checks = append(env.Checks, query.Check{Name: "database", OK: false, Detail: "serve file present but unreadable: " + err.Error()})
		env.Checks = append(env.Checks, query.Check{Name: "fts", OK: false, Detail: "serve file unreadable"})
	} else {
		dbCheck, db := checkDB(cfg.DB)
		env.Checks = append(env.Checks, dbCheck)
		if db != nil {
			defer db.Close()
			if e, err := query.Status(ctx, db); err == nil {
				env.LastSync = e.LastSync
				env.BodyCoverage = e.BodyCoverage
				env.SchemaVersion = e.SchemaVersion
				env.LastError = e.LastError
			}
			env.Checks = append(env.Checks, checkFTS(ctx, db))
		} else {
			env.Checks = append(env.Checks, query.Check{Name: "fts", OK: false, Detail: "database not open"})
		}
	}
	env.Checks = append(env.Checks, checkPort("oauth_port", cfg.OAuthPort, cfg.DB))
	port := 8080
	if n, err := strconv.Atoi(cfg.Port); err == nil {
		port = n
	}
	env.Checks = append(env.Checks, checkPort("ui_port", port, cfg.DB))
	env.Checks = append(env.Checks, checkServe(cfg.DB))
	env.ResultCount = len(env.Checks)
	return env
}

func ftsDetail(ok bool) string {
	if ok {
		return "search index ready"
	}
	return "search index pending; literal search works"
}

func DoctorOK(env query.Envelope) bool {
	for _, c := range env.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

func PrintSteps(w io.Writer) {
	fmt.Fprint(w, SetupSteps)
}

func checkDesktopCreds(raw []byte) error {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("credentials JSON: %w", err)
	}
	if _, ok := doc["installed"]; !ok {
		return fmt.Errorf("credentials must be a Desktop app client (key \"installed\")")
	}
	return nil
}

func checkCredentials(path string) query.Check {
	c := query.Check{Name: "credentials"}
	raw, err := os.ReadFile(path)
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	if err := checkDesktopCreds(raw); err != nil {
		c.Detail = err.Error()
		return c
	}
	c.OK = true
	c.Detail = path
	return c
}

func checkToken(path string) query.Check {
	c := query.Check{Name: "token"}
	if err := privfile.Check(path); err != nil {
		c.Detail = err.Error()
		return c
	}
	raw, err := privfile.Read(path)
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	var tok struct {
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		c.Detail = err.Error()
		return c
	}
	if tok.RefreshToken == "" {
		c.Detail = "missing refresh_token"
		return c
	}
	if tok.Scope != "" && !strings.Contains(tok.Scope, gmailapi.GmailReadonlyScope) {
		c.Detail = "token scope lacks gmail.readonly"
		return c
	}
	c.OK = true
	c.Detail = path + " (local check only: presence, shape, and scope; cannot prove Google will refresh this token)"
	return c
}

func checkDB(path string) (query.Check, *store.DB) {
	c := query.Check{Name: "database"}
	if _, err := os.Stat(path); err != nil {
		c.Detail = err.Error()
		return c, nil
	}
	db, err := store.Open(path)
	if err != nil {
		c.Detail = err.Error()
		return c, nil
	}
	c.OK = true
	c.Detail = path
	return c, db
}

func checkFTS(ctx context.Context, db *store.DB) query.Check {
	c := query.Check{Name: "fts", OK: true}
	ok, err := db.HasFTS(ctx)
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	c.Detail = ftsDetail(ok)
	return c
}

func checkPort(name string, port int, dbPath string) query.Check {
	c := query.Check{Name: name}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err == nil {
		_ = ln.Close()
		c.OK = true
		c.Detail = fmt.Sprintf("127.0.0.1:%d free", port)
		return c
	}
	if info, e := web.ReadServeFile(dbPath); e == nil && serveUsesPort(info, port) {
		c.OK = true
		c.Detail = fmt.Sprintf("127.0.0.1:%d in use by serve", port)
		return c
	}
	c.Detail = err.Error()
	return c
}

func serveUsesPort(info web.ServeInfo, port int) bool {
	u, err := url.Parse(info.URL)
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(u.Port())
	return err == nil && n == port && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")
}

func checkServe(dbPath string) query.Check {
	c := query.Check{Name: "serve"}
	if _, err := web.ReadServeFile(dbPath); err != nil {
		if os.IsNotExist(err) {
			c.OK = true
			c.Detail = "serve not running"
			return c
		}
		c.Detail = "serve file present but unreadable: " + err.Error()
		return c
	}
	if _, err := web.Dial(dbPath); err != nil {
		c.Detail = "serve file present but not reachable: " + err.Error()
		return c
	}
	c.OK = true
	c.Detail = "serve reachable"
	return c
}
