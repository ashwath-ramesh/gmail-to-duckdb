package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
	"golang.org/x/time/rate"
)

const defaultAPIURL = "https://gmail.googleapis.com/gmail/v1/users/me/"

// maxErrorBody limits how much of an error reply is read.
const maxErrorBody = 64 << 10

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

// Client calls the Gmail REST API with an authorized HTTP client.
type Client struct {
	http     *http.Client
	limiter  *rate.Limiter
	apiURL   string
	batchURL string
	waitHook func(context.Context, int) error
}

func New(hc *http.Client) *Client {
	return &Client{
		http:     hc,
		limiter:  rate.NewLimiter(rate.Limit(quotaPerSec), quotaBurst),
		apiURL:   defaultAPIURL,
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

// get sends one GET to path under the API URL, with quota wait and retries,
// and returns the reply body.
func (c *Client) get(ctx context.Context, path string, q url.Values, quota int) ([]byte, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("prettyPrint", "false")
	u := c.apiURL + path + "?" + q.Encode()
	var body []byte
	err := retry(ctx, func() error {
		if err := c.waitQuota(ctx, quota); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
			return statusErr(resp.StatusCode, string(b), resp.Header)
		}
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		body = b
		return nil
	})
	return body, err
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, quota int, out any) error {
	b, err := c.get(ctx, path, q, quota)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (c *Client) Profile(ctx context.Context) (Profile, error) {
	var r struct {
		EmailAddress string `json:"emailAddress"`
		HistoryID    uint64 `json:"historyId,string"`
	}
	if err := c.getJSON(ctx, "profile", nil, 1, &r); err != nil {
		return Profile{}, mapAPIError(err)
	}
	return Profile{Email: r.EmailAddress, HistoryID: r.HistoryID}, nil
}

func (c *Client) Labels(ctx context.Context) ([]store.Label, error) {
	var r struct {
		Labels []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"labels"`
	}
	if err := c.getJSON(ctx, "labels", nil, 1, &r); err != nil {
		return nil, mapAPIError(err)
	}
	out := make([]store.Label, 0, len(r.Labels))
	for _, l := range r.Labels {
		out = append(out, store.Label{ID: l.ID, Name: l.Name, Type: l.Type})
	}
	return out, nil
}

func (c *Client) ListMessages(ctx context.Context, pageToken string) ([]string, string, error) {
	q := url.Values{"maxResults": {"500"}, "includeSpamTrash": {"true"}}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	var r struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := c.getJSON(ctx, "messages", q, quotaPerList, &r); err != nil {
		return nil, "", mapListError(err)
	}
	ids := make([]string, 0, len(r.Messages))
	for _, m := range r.Messages {
		ids = append(ids, m.ID)
	}
	return ids, r.NextPageToken, nil
}

func (c *Client) History(ctx context.Context, startID uint64, pageToken string) (HistoryPage, error) {
	q := url.Values{"startHistoryId": {strconv.FormatUint(startID, 10)}}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	var r historyReply
	if err := c.getJSON(ctx, "history", q, quotaPerHist, &r); err != nil {
		if classifyError(err) == classNotFound {
			return HistoryPage{}, ErrHistoryGone
		}
		return HistoryPage{}, mapListError(err)
	}
	return historyPage(r), nil
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
	raw, err := c.get(ctx, "messages/"+url.PathEscape(id), url.Values{"format": {format}}, quotaPerGet)
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
