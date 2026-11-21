package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/query"
	mailsync "github.com/ashwath-ramesh/gmail-to-duckdb/internal/sync"
)

type Client struct {
	info ServeInfo
	http *http.Client
}

func Dial(dbPath string) (*Client, error) {
	info, err := ReadServeFile(dbPath)
	if err != nil {
		return nil, err
	}
	if err := ValidateServeURL(info.URL); err != nil {
		return nil, err
	}
	if info.DB != "" {
		want, err := filepath.Abs(dbPath)
		if err != nil {
			return nil, err
		}
		if filepath.Clean(info.DB) != want {
			return nil, otherDatabaseError{serveDB: info.DB, want: want}
		}
	}
	c := &Client{info: info, http: &http.Client{Timeout: 30 * time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if err := c.Health(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) Health(ctx context.Context) error {
	_, err := c.get(ctx, "/api/health", nil)
	return err
}

func (c *Client) Close() error { return nil }

func (c *Client) Status(ctx context.Context) (query.Envelope, error) {
	return c.get(ctx, "/api/status", nil)
}

func (c *Client) Search(ctx context.Context, q string) (query.Envelope, error) {
	return c.get(ctx, "/api/messages", url.Values{"q": {q}})
}

func (c *Client) Get(ctx context.Context, id string, body bool) (query.Envelope, error) {
	q := url.Values{}
	if body {
		q.Set("body", "1")
	}
	return c.get(ctx, "/api/messages/"+url.PathEscape(id), q)
}

func (c *Client) Schema(ctx context.Context) (query.Envelope, error) {
	return c.get(ctx, "/api/schema", nil)
}

func (c *Client) SQL(ctx context.Context, q string, write bool) (query.Envelope, error) {
	return c.post(ctx, "/api/sql", map[string]any{"query": q, "write": write})
}

func (c *Client) SyncNow(ctx context.Context, opt mailsync.Options) (query.Envelope, error) {
	wait := *c
	wait.http = &http.Client{}
	return wait.post(ctx, "/api/sync", map[string]any{"full": opt.Full, "bodies": opt.Bodies, "wait": true})
}

// LoginLink asks the running serve for a one-time UI login link.
func (c *Client) LoginLink(ctx context.Context) (string, error) {
	var out struct {
		Code string `json:"code"`
	}
	if err := c.send(ctx, http.MethodPost, "/api/login-code", nil, struct{}{}, &out); err != nil {
		return "", err
	}
	if out.Code == "" {
		return "", fmt.Errorf("serve returned no login code")
	}
	return LoginLink(c.info.URL, out.Code), nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values) (query.Envelope, error) {
	var env query.Envelope
	err := c.send(ctx, http.MethodGet, path, q, nil, &env)
	return env, err
}

func (c *Client) post(ctx context.Context, path string, body any) (query.Envelope, error) {
	var env query.Envelope
	err := c.send(ctx, http.MethodPost, path, nil, body, &env)
	return env, err
}

func (c *Client) send(ctx context.Context, method, path string, q url.Values, body, out any) error {
	u, err := url.Parse(c.info.URL)
	if err != nil {
		return err
	}
	u.Path = path
	if q != nil {
		u.RawQuery = q.Encode()
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.info.Token != "" {
		req.Header.Set("X-Token", c.info.Token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = string(b)
		}
		return fmt.Errorf("%s", e.Error)
	}
	return json.Unmarshal(b, out)
}
