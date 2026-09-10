package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

func TestListMessagesIncludeSpamTrash(t *testing.T) {
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("includeSpamTrash")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messages": []map[string]string{{"id": "m1"}},
		})
	}))
	t.Cleanup(ts.Close)
	c := testGmailClient(t, ts)
	ids, next, err := c.ListMessages(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "true" {
		t.Fatalf("includeSpamTrash=%q", got)
	}
	if len(ids) != 1 || ids[0] != "m1" || next != "" {
		t.Fatalf("ids=%v next=%q", ids, next)
	}
}

func TestGetMetadataOmitsHeaderAllowlist(t *testing.T) {
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "m1", "threadId": "t", "labelIds": []string{"INBOX"},
		})
	}))
	t.Cleanup(ts.Close)
	c := testGmailClient(t, ts)
	res, err := c.Get(context.Background(), "m1", "metadata")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != FetchOK {
		t.Fatalf("%+v", res)
	}
	if strings.Contains(got, "metadataHeaders") {
		t.Fatalf("metadata must request all headers: %s", got)
	}
}

func TestGet404IsNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, 404, "notFound", "Requested entity was not found.")
	}))
	t.Cleanup(ts.Close)
	c := testGmailClient(t, ts)
	res, err := c.Get(context.Background(), "gone", "metadata")
	if err != nil {
		t.Fatalf("404 must not be a transport error: %v", err)
	}
	if res.ID != "gone" || res.Status != FetchNotFound {
		t.Fatalf("%+v", res)
	}
}

func TestGetAuthSurfaces(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, 403, "forbidden", "forbidden")
	}))
	t.Cleanup(ts.Close)
	c := testGmailClient(t, ts)
	res, err := c.Get(context.Background(), "m", "metadata")
	if err == nil {
		t.Fatal("auth must surface")
	}
	if !strings.Contains(err.Error(), "auth") && classifyError(err) != classAuth {
		t.Fatalf("err %v res %+v", err, res)
	}
}

func TestRetryChargesLimiterEachAttempt(t *testing.T) {
	old := retries
	t.Cleanup(func() { retries = old })
	retries.attempts = 4
	retries.initial = time.Millisecond
	retries.max = time.Millisecond
	retries.sleep = func(context.Context, time.Duration) error { return nil }

	var n atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := n.Add(1)
		if i < 3 {
			w.Header().Set("Retry-After", "1")
			writeAPIError(w, 429, "rateLimitExceeded", "rateLimitExceeded")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "m1", "threadId": "t", "labelIds": []string{"INBOX"},
		})
	}))
	t.Cleanup(ts.Close)
	c := testGmailClient(t, ts)
	var waits int
	c.waitHook = func(ctx context.Context, units int) error {
		waits++
		return c.limiter.WaitN(ctx, units)
	}
	res, err := c.Get(context.Background(), "m1", "metadata")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != FetchOK {
		t.Fatalf("%+v", res)
	}
	if waits < 3 || int(n.Load()) < 3 {
		t.Fatalf("waits=%d http=%d", waits, n.Load())
	}
}

func TestBatchGetRetryableThenOK(t *testing.T) {
	old := retries
	t.Cleanup(func() { retries = old })
	retries.attempts = 4
	retries.sleep = func(context.Context, time.Duration) error { return nil }

	var n atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = body
		i := n.Add(1)
		if strings.Contains(r.URL.Path, "/batch") || r.URL.Path == "/" || strings.Contains(r.URL.Path, "batch") {
			if i < 2 {
				w.Header().Set("Retry-After", "1")
				http.Error(w, `{"error":{"code":403,"errors":[{"reason":"userRateLimitExceeded"}]}}`, 403)
				return
			}
			w.Header().Set("Content-Type", "multipart/mixed; boundary=batch_b")
			_, _ = io.WriteString(w, part("response-0", 200, `{"id":"aaa"}`))
			_, _ = io.WriteString(w, "--batch_b--\r\n")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)
	c := testGmailClient(t, ts)
	c.batchURL = ts.URL
	var waits int
	c.waitHook = func(ctx context.Context, units int) error {
		waits++
		return nil
	}
	parts, err := c.BatchGet(context.Background(), []string{"aaa"}, "metadata")
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].Status != FetchOK || parts[0].ID != "aaa" {
		t.Fatalf("%+v", parts)
	}
	if waits < 2 {
		t.Fatalf("retries must charge limiter: waits=%d http=%d", waits, n.Load())
	}
}

func TestBatchGetInnerRetryAfterThenOK(t *testing.T) {
	old := retries
	t.Cleanup(func() { retries = old })
	var slept []time.Duration
	retries.attempts = 4
	retries.initial = time.Hour
	retries.max = time.Hour
	retries.sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	var n atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := n.Add(1)
		w.Header().Set("Content-Type", "multipart/mixed; boundary=batch_b")
		if i == 1 {
			_, _ = io.WriteString(w, partRetryAfter("response-0", 429, 9, `{"error":{"code":429}}`))
			_, _ = io.WriteString(w, partRetryAfter("response-1", 503, 3, `{"error":{"code":503}}`))
			_, _ = io.WriteString(w, "--batch_b--\r\n")
			return
		}
		_, _ = io.WriteString(w, part("response-0", 200, `{"id":"aaa"}`))
		_, _ = io.WriteString(w, part("response-1", 200, `{"id":"bbb"}`))
		_, _ = io.WriteString(w, "--batch_b--\r\n")
	}))
	t.Cleanup(ts.Close)
	c := testGmailClient(t, ts)
	c.batchURL = ts.URL
	var waits int
	c.waitHook = func(ctx context.Context, units int) error {
		waits++
		return nil
	}
	parts, err := c.BatchGet(context.Background(), []string{"aaa", "bbb"}, "metadata")
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0].Status != FetchOK || parts[1].Status != FetchOK {
		t.Fatalf("%+v", parts)
	}
	if n.Load() < 2 {
		t.Fatalf("http %d", n.Load())
	}
	if waits < 2 {
		t.Fatalf("quota charges %d", waits)
	}
	if len(slept) != 1 || slept[0] != 9*time.Second {
		t.Fatalf("inner Retry-After lost: slept=%v", slept)
	}
}

func TestProfileAndLabelsMeterAttempts(t *testing.T) {
	old := retries
	t.Cleanup(func() { retries = old })
	retries.attempts = 4
	retries.sleep = func(context.Context, time.Duration) error { return nil }
	var n atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := n.Add(1)
		if i == 1 || i == 3 {
			writeAPIError(w, 429, "rateLimitExceeded", "slow")
			return
		}
		if strings.Contains(r.URL.Path, "labels") {
			_ = json.NewEncoder(w).Encode(map[string]any{"labels": []map[string]string{{"id": "INBOX", "name": "Inbox"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"emailAddress": "me@example.com", "historyId": "1"})
	}))
	t.Cleanup(ts.Close)
	c := testGmailClient(t, ts)
	var waits int
	c.waitHook = func(ctx context.Context, units int) error {
		waits++
		return nil
	}
	if _, err := c.Profile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Labels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if waits < 4 {
		t.Fatalf("profile/labels retries must charge limiter: waits=%d http=%d", waits, n.Load())
	}
}

func TestGetEmptyAndMalformedIDRejected(t *testing.T) {
	var n atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"threadId": "t"})
			return
		}
		_, _ = io.WriteString(w, `{`)
	}))
	t.Cleanup(ts.Close)
	c := testGmailClient(t, ts)
	empty, err := c.Get(context.Background(), "aaa", "metadata")
	if err == nil && empty.Status == FetchOK {
		t.Fatalf("empty id OK: %+v", empty)
	}
	bad, err := c.Get(context.Background(), "aaa", "metadata")
	if err == nil && bad.Status == FetchOK {
		t.Fatalf("malformed OK: %+v", bad)
	}
}

func TestBatchGetRetainsCompletedChunkOnCancel(t *testing.T) {
	old := retries
	t.Cleanup(func() { retries = old })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	retries.sleep = func(ctx context.Context, d time.Duration) error {
		cancel()
		return ctx.Err()
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/mixed; boundary=batch_b")
		for i := 0; i < 10; i++ {
			id := string(rune('a' + i))
			_, _ = io.WriteString(w, part("response-"+strconv.Itoa(i), 200, `{"id":"`+id+`"}`))
		}
		_, _ = io.WriteString(w, "--batch_b--\r\n")
	}))
	t.Cleanup(ts.Close)
	c := testGmailClient(t, ts)
	c.batchURL = ts.URL
	ids := make([]string, 11)
	for i := range ids {
		ids[i] = string(rune('a' + i))
	}
	parts, err := c.BatchGet(ctx, ids, "metadata")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if len(parts) < 10 {
		t.Fatalf("lost completed chunk: %d", len(parts))
	}
}

func partRetryAfter(cid string, code int, sec int, body string) string {
	status := "Too Many Requests"
	if code == 503 {
		status = "Service Unavailable"
	}
	return "--batch_b\r\n" +
		"Content-Type: application/http\r\n" +
		"Content-ID: <" + cid + ">\r\n" +
		"\r\n" +
		"HTTP/1.1 " + strconv.Itoa(code) + " " + status + "\r\n" +
		"Retry-After: " + strconv.Itoa(sec) + "\r\n" +
		"Content-Type: application/json\r\n" +
		"\r\n" +
		body + "\r\n"
}

func writeAPIError(w http.ResponseWriter, code int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"errors":  []map[string]string{{"reason": reason, "message": message}},
		},
	})
}

func testGmailClient(t *testing.T, ts *httptest.Server) *Client {
	t.Helper()
	svc, err := gmailapi.NewService(context.Background(),
		option.WithEndpoint(ts.URL+"/"),
		option.WithoutAuthentication(),
		option.WithHTTPClient(ts.Client()),
	)
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(svc, ts.Client())
	c.limiter = rate.NewLimiter(rate.Inf, 1000)
	return c
}
