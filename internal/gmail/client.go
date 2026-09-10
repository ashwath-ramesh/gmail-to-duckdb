package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	"golang.org/x/time/rate"
	gmailapi "google.golang.org/api/gmail/v1"
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
	BatchGet(ctx context.Context, ids []string, format string) ([]FetchResult, error)
	Get(ctx context.Context, id, format string) (FetchResult, error)
}

type Client struct {
	svc      *gmailapi.Service
	http     *http.Client
	limiter  *rate.Limiter
	batchURL string
	waitHook func(context.Context, int) error
}

func New(ctx context.Context, hc *http.Client) (*Client, error) {
	svc, err := gmailapi.NewService(ctx, option.WithHTTPClient(hc))
	if err != nil {
		return nil, err
	}
	return newClient(svc, hc), nil
}

func newClient(svc *gmailapi.Service, hc *http.Client) *Client {
	return &Client{
		svc:      svc,
		http:     hc,
		limiter:  rate.NewLimiter(rate.Limit(quotaPerSec), quotaBurst),
		batchURL: defaultBatchURL,
	}
}

func (c *Client) waitQuota(ctx context.Context, n int) error {
	if n <= 0 {
		n = 1
	}
	if c.waitHook != nil {
		return c.waitHook(ctx, n)
	}
	return c.limiter.WaitN(ctx, n)
}

func (c *Client) Profile(ctx context.Context) (Profile, error) {
	var p *gmailapi.Profile
	err := retry(ctx, func() error {
		if err := c.waitQuota(ctx, 1); err != nil {
			return err
		}
		var e error
		p, e = c.svc.Users.GetProfile("me").Context(ctx).Do()
		return e
	})
	if err != nil {
		return Profile{}, mapAPIError(err)
	}
	return Profile{Email: p.EmailAddress, HistoryID: p.HistoryId}, nil
}

func (c *Client) Labels(ctx context.Context) ([]store.Label, error) {
	var r *gmailapi.ListLabelsResponse
	err := retry(ctx, func() error {
		if err := c.waitQuota(ctx, 1); err != nil {
			return err
		}
		var e error
		r, e = c.svc.Users.Labels.List("me").Context(ctx).Do()
		return e
	})
	if err != nil {
		return nil, mapAPIError(err)
	}
	out := make([]store.Label, 0, len(r.Labels))
	for _, l := range r.Labels {
		out = append(out, store.Label{ID: l.Id, Name: l.Name, Type: l.Type})
	}
	return out, nil
}

func (c *Client) ListMessages(ctx context.Context, pageToken string) ([]string, string, error) {
	var r *gmailapi.ListMessagesResponse
	err := retry(ctx, func() error {
		if err := c.waitQuota(ctx, quotaPerList); err != nil {
			return err
		}
		call := c.svc.Users.Messages.List("me").MaxResults(500).IncludeSpamTrash(true).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		var e error
		r, e = call.Do()
		return e
	})
	if err != nil {
		return nil, "", mapListError(err)
	}
	ids := make([]string, 0, len(r.Messages))
	for _, m := range r.Messages {
		ids = append(ids, m.Id)
	}
	return ids, r.NextPageToken, nil
}

func (c *Client) History(ctx context.Context, startID uint64, pageToken string) (HistoryPage, error) {
	var r *gmailapi.ListHistoryResponse
	err := retry(ctx, func() error {
		if err := c.waitQuota(ctx, quotaPerHist); err != nil {
			return err
		}
		call := c.svc.Users.History.List("me").StartHistoryId(startID).Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		var e error
		r, e = call.Do()
		return e
	})
	if err != nil {
		if classifyError(err) == classNotFound {
			return HistoryPage{}, ErrHistoryGone
		}
		return HistoryPage{}, mapListError(err)
	}
	raw, err := r.MarshalJSON()
	if err != nil {
		return HistoryPage{}, err
	}
	return parseHistory(raw)
}

func (c *Client) BatchGet(ctx context.Context, ids []string, format string) ([]FetchResult, error) {
	if format == "" {
		format = "metadata"
	}
	var all []FetchResult
	chunks := splitIDs(ids, maxBatchSize)
	for i, chunk := range chunks {
		if i > 0 {
			if err := retries.sleep(ctx, batchGap); err != nil {
				return all, err
			}
		}
		parts, err := c.batchGetOnce(ctx, chunk, format)
		all = append(all, parts...)
		if err != nil {
			return all, err
		}
	}
	return all, nil
}

func (c *Client) batchGetOnce(ctx context.Context, ids []string, format string) ([]FetchResult, error) {
	body, ct, err := encodeBatchGet(ids, format)
	if err != nil {
		return nil, err
	}
	var last []FetchResult
	err = retry(ctx, func() error {
		if err := c.waitQuota(ctx, quotaPerGet*len(ids)); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.batchURL, bytes.NewReader(body))
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
		if resp.StatusCode >= 400 {
			return statusErr(resp.StatusCode, string(raw), resp.Header)
		}
		parts, err := decodeBatchBody(raw, resp.Header.Get("Content-Type"), ids)
		if err != nil {
			return err
		}
		last = parts
		if err := retryablePartError(parts); err != nil {
			return err
		}
		return nil
	})
	if last == nil && err != nil {
		last = unresolvedChunk(ids, err)
	}
	if err != nil {
		return last, mapAPIError(err)
	}
	return last, nil
}

func (c *Client) Get(ctx context.Context, id, format string) (FetchResult, error) {
	if format == "" {
		format = "full"
	}
	var msg *gmailapi.Message
	err := retry(ctx, func() error {
		if err := c.waitQuota(ctx, quotaPerGet); err != nil {
			return err
		}
		call := c.svc.Users.Messages.Get("me", id).Format(format).Context(ctx)
		var e error
		msg, e = call.Do()
		return e
	})
	if err != nil {
		switch classifyError(err) {
		case classNotFound:
			return FetchResult{ID: id, Status: FetchNotFound}, nil
		case classAuth:
			return FetchResult{ID: id, Status: FetchFatal, Err: err}, mapAPIError(err)
		case classRetryable:
			return FetchResult{ID: id, Status: FetchRetryable, Err: err}, nil
		default:
			return FetchResult{ID: id, Status: FetchFatal, Err: err}, err
		}
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return FetchResult{ID: id, Status: FetchFatal, Err: err}, err
	}
	if err := MatchMessageID(raw, id); err != nil {
		return FetchResult{ID: id, Status: FetchFatal, Err: err}, nil
	}
	return FetchResult{ID: id, Status: FetchOK, Raw: raw}, nil
}

func mapListError(err error) error {
	switch classifyError(err) {
	case classPageToken:
		return ErrPageTokenExpired
	case classAuth:
		return mapAPIError(err)
	default:
		return err
	}
}

func retryablePartError(parts []FetchResult) error {
	var best error
	var wait time.Duration
	for _, p := range parts {
		if p.Status != FetchRetryable || p.Err == nil {
			continue
		}
		w := retryAfterOf(p.Err)
		if best == nil || w > wait {
			best = p.Err
			wait = w
		}
	}
	return best
}

func unresolvedChunk(ids []string, err error) []FetchResult {
	out := make([]FetchResult, 0, len(ids))
	st := FetchRetryable
	if !isRetryable(err) {
		st = FetchFatal
	}
	for _, id := range ids {
		out = append(out, FetchResult{ID: id, Status: st, Err: err})
	}
	return out
}
