package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	"golang.org/x/time/rate"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

type Profile struct {
	Email     string
	HistoryID uint64
}

// API is the Gmail surface the sync loop needs. Tests inject a fake.
type API interface {
	Profile(ctx context.Context) (Profile, error)
	Labels(ctx context.Context) ([]store.Label, error)
	ListMessages(ctx context.Context, pageToken string) (ids []string, next string, err error)
	History(ctx context.Context, startID uint64, pageToken string) (HistoryPage, error)
	BatchGet(ctx context.Context, ids []string, format string) ([][]byte, error)
	Get(ctx context.Context, id, format string) ([]byte, error)
}

type Client struct {
	svc     *gmailapi.Service
	http    *http.Client
	limiter *rate.Limiter
}

func New(ctx context.Context, hc *http.Client) (*Client, error) {
	svc, err := gmailapi.NewService(ctx, option.WithHTTPClient(hc))
	if err != nil {
		return nil, err
	}
	return &Client{
		svc:     svc,
		http:    hc,
		limiter: rate.NewLimiter(rate.Limit(quotaPerSec), quotaBurst),
	}, nil
}

func (c *Client) Profile(ctx context.Context) (Profile, error) {
	var p *gmailapi.Profile
	err := retry(ctx, func() error {
		var e error
		p, e = c.svc.Users.GetProfile("me").Context(ctx).Do()
		return e
	})
	if err != nil {
		return Profile{}, err
	}
	return Profile{Email: p.EmailAddress, HistoryID: p.HistoryId}, nil
}

func (c *Client) Labels(ctx context.Context) ([]store.Label, error) {
	var r *gmailapi.ListLabelsResponse
	err := retry(ctx, func() error {
		var e error
		r, e = c.svc.Users.Labels.List("me").Context(ctx).Do()
		return e
	})
	if err != nil {
		return nil, err
	}
	out := make([]store.Label, 0, len(r.Labels))
	for _, l := range r.Labels {
		out = append(out, store.Label{ID: l.Id, Name: l.Name, Type: l.Type})
	}
	return out, nil
}

func (c *Client) ListMessages(ctx context.Context, pageToken string) ([]string, string, error) {
	if err := c.limiter.WaitN(ctx, quotaPerList); err != nil {
		return nil, "", err
	}
	var r *gmailapi.ListMessagesResponse
	err := retry(ctx, func() error {
		call := c.svc.Users.Messages.List("me").MaxResults(500).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		var e error
		r, e = call.Do()
		return e
	})
	if err != nil {
		return nil, "", err
	}
	ids := make([]string, 0, len(r.Messages))
	for _, m := range r.Messages {
		ids = append(ids, m.Id)
	}
	return ids, r.NextPageToken, nil
}

func (c *Client) History(ctx context.Context, startID uint64, pageToken string) (HistoryPage, error) {
	if err := c.limiter.WaitN(ctx, quotaPerHist); err != nil {
		return HistoryPage{}, err
	}
	var r *gmailapi.ListHistoryResponse
	err := retry(ctx, func() error {
		call := c.svc.Users.History.List("me").StartHistoryId(startID).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		var e error
		r, e = call.Do()
		return e
	})
	if err != nil {
		if isHTTPStatus(err, 404) {
			return HistoryPage{}, ErrHistoryGone
		}
		return HistoryPage{}, err
	}
	raw, err := r.MarshalJSON()
	if err != nil {
		return HistoryPage{}, err
	}
	return parseHistory(raw)
}

func (c *Client) BatchGet(ctx context.Context, ids []string, format string) ([][]byte, error) {
	if format == "" {
		format = "metadata"
	}
	var all [][]byte
	chunks := splitIDs(ids, maxBatchSize)
	for i, chunk := range chunks {
		if i > 0 {
			timer := time.NewTimer(300 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		parts, err := c.batchGetOnce(ctx, chunk, format)
		if err != nil {
			return nil, err
		}
		all = append(all, parts...)
	}
	return all, nil
}

func (c *Client) batchGetOnce(ctx context.Context, ids []string, format string) ([][]byte, error) {
	if err := c.limiter.WaitN(ctx, quotaPerGet*len(ids)); err != nil {
		return nil, err
	}
	body, ct, err := encodeBatchGet(ids, format)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	err = retry(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, batchURL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", ct)
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			return fmt.Errorf("batch status %d: %s", resp.StatusCode, bytes.TrimSpace(raw))
		}
		if resp.StatusCode >= 400 {
			return fmt.Errorf("batch status %d: %s", resp.StatusCode, bytes.TrimSpace(raw))
		}
		parts, err := decodeBatchBody(raw, resp.Header.Get("Content-Type"))
		if err != nil {
			return err
		}
		out = parts
		return nil
	})
	return out, err
}

func (c *Client) Get(ctx context.Context, id, format string) ([]byte, error) {
	if format == "" {
		format = "full"
	}
	if err := c.limiter.WaitN(ctx, quotaPerGet); err != nil {
		return nil, err
	}
	var msg *gmailapi.Message
	err := retry(ctx, func() error {
		call := c.svc.Users.Messages.Get("me", id).Format(format).Context(ctx)
		if format == "metadata" {
			call = call.MetadataHeaders(metadataHeaders...)
		}
		var e error
		msg, e = call.Do()
		return e
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(msg)
}

func retry(ctx context.Context, fn func() error) error {
	var err error
	backoff := 2 * time.Second
	for attempt := 0; attempt < 8; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = fn()
		if err == nil {
			return nil
		}
		if !retryable(err) {
			return err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
	return err
}

func retryable(err error) bool {
	if err == nil {
		return false
	}
	if isHTTPStatus(err, 429) || isHTTPStatus(err, 500) || isHTTPStatus(err, 502) || isHTTPStatus(err, 503) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "429") ||
		strings.Contains(s, "rateLimitExceeded") ||
		strings.Contains(s, "RESOURCE_EXHAUSTED") ||
		strings.Contains(s, "batch status 5")
}

func isHTTPStatus(err error, code int) bool {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		return gerr.Code == code
	}
	return false
}
