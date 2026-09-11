package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

func (c *Client) get(ctx context.Context, path string, q url.Values) (query.Envelope, error) {
	u, err := url.Parse(c.info.URL)
	if err != nil {
		return query.Envelope{}, err
	}
	u.Path = path
	if q != nil {
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return query.Envelope{}, err
	}
	return c.do(req)
}

func (c *Client) post(ctx context.Context, path string, body any) (query.Envelope, error) {
	u, err := url.Parse(c.info.URL)
	if err != nil {
		return query.Envelope{}, err
	}
	u.Path = path
	raw, err := json.Marshal(body)
	if err != nil {
		return query.Envelope{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(raw))
	if err != nil {
		return query.Envelope{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req)
}

func (c *Client) do(req *http.Request) (query.Envelope, error) {
	if c.info.Token != "" {
		req.Header.Set("X-Token", c.info.Token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return query.Envelope{}, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return query.Envelope{}, err
	}
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = string(b)
		}
		return query.Envelope{}, fmt.Errorf("%s", e.Error)
	}
	var env query.Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return query.Envelope{}, err
	}
	return env, nil
}
