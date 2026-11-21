package gmail

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
)

func TestClassifyRetryableAndAuth(t *testing.T) {
	quota := &googleapi.Error{Code: 403, Errors: []googleapi.ErrorItem{{Reason: "rateLimitExceeded"}}}
	if classifyError(quota) != classRetryable || !isRetryable(quota) {
		t.Fatalf("quota 403: %v", classifyError(quota))
	}
	perm := &googleapi.Error{Code: 403, Message: "forbidden", Errors: []googleapi.ErrorItem{{Reason: "forbidden"}}}
	if classifyError(perm) != classAuth || isRetryable(perm) {
		t.Fatalf("perm 403: %v", classifyError(perm))
	}
	unauth := &googleapi.Error{Code: 401, Message: "unauthorized"}
	if classifyError(unauth) != classAuth || isRetryable(unauth) {
		t.Fatalf("401: %v", classifyError(unauth))
	}
	if classifyError(&googleapi.Error{Code: 429}) != classRetryable {
		t.Fatal("429")
	}
	if classifyError(&googleapi.Error{Code: 503}) != classRetryable {
		t.Fatal("503")
	}
	if classifyError(&googleapi.Error{Code: 404}) != classNotFound {
		t.Fatal("404")
	}
	if classifyError(&googleapi.Error{Code: 400, Message: "Invalid page token"}) != classPageToken {
		t.Fatal("page token")
	}
	if classifyError(io.EOF) != classRetryable || classifyError(io.ErrUnexpectedEOF) != classRetryable {
		t.Fatal("eof")
	}
	var ne net.Error = timeoutErr{}
	if classifyError(ne) != classRetryable {
		t.Fatal("timeout")
	}
	var reset net.Error = resetErr{}
	if classifyError(reset) != classRetryable {
		t.Fatal("reset")
	}
	if classifyError(&url.Error{Op: "Get", URL: "https://gmail", Err: reset}) != classRetryable {
		t.Fatal("wrapped reset")
	}
	if classifyError(context.Canceled) != classFatal {
		t.Fatal("cancel must not retry")
	}
}

func TestParseRetryAfter(t *testing.T) {
	h := make(http.Header)
	h.Set("Retry-After", "7")
	if d := parseRetryAfter(h); d != 7*time.Second {
		t.Fatalf("seconds %s", d)
	}
	err := statusErr(429, "slow", h)
	if d := retryAfterOf(err); d != 7*time.Second {
		t.Fatalf("status %s", d)
	}
}

func TestRetryHonorsRetryAfter(t *testing.T) {
	old := retries
	t.Cleanup(func() { retries = old })
	var slept []time.Duration
	retries.attempts = 3
	retries.initial = time.Hour
	retries.sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	h := make(http.Header)
	h.Set("Retry-After", "4")
	n := 0
	err := retry(context.Background(), func() error {
		n++
		if n < 2 {
			return statusErr(429, "wait", h)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(slept) != 1 || slept[0] != 4*time.Second {
		t.Fatalf("n=%d slept=%v", n, slept)
	}
}

func TestOAuthURLErrorIsNotRetryable(t *testing.T) {
	re := &oauth2.RetrieveError{ErrorCode: "invalid_grant", ErrorDescription: "expired"}
	wrapped := &url.Error{Op: "Post", URL: "https://oauth2.googleapis.com/token", Err: re}
	if isRetryable(wrapped) {
		t.Fatal("oauth url.Error must not retry")
	}
	if classifyError(wrapped) == classRetryable {
		t.Fatal("oauth classified retryable")
	}
}

func TestRetryExhaustionDoesNotSleepAfterLast(t *testing.T) {
	old := retries
	t.Cleanup(func() { retries = old })
	var slept []time.Duration
	retries.attempts = 2
	retries.initial = time.Second
	retries.sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	n := 0
	err := retry(context.Background(), func() error {
		n++
		return statusErr(429, "slow", nil)
	})
	if err == nil {
		t.Fatal("exhausted retry must fail")
	}
	if n != 2 {
		t.Fatalf("attempts %d", n)
	}
	if len(slept) != 1 {
		t.Fatalf("must not sleep after last attempt: slept=%v", slept)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

type resetErr struct{}

func (resetErr) Error() string   { return "read: connection reset by peer" }
func (resetErr) Timeout() bool   { return false }
func (resetErr) Temporary() bool { return true }

func TestRetryCapsRetryAfter(t *testing.T) {
	old := retries
	t.Cleanup(func() { retries = old })
	var slept []time.Duration
	retries.attempts = 2
	retries.sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	h := make(http.Header)
	h.Set("Retry-After", "86400")
	n := 0
	_ = retry(context.Background(), func() error {
		n++
		if n < 2 {
			return statusErr(429, "wait", h)
		}
		return nil
	})
	if len(slept) != 1 || slept[0] != maxRetryAfter {
		t.Fatalf("slept=%v want %v", slept, maxRetryAfter)
	}
}

func TestRetryRetriesRequestTimeout(t *testing.T) {
	old := retries
	t.Cleanup(func() { retries = old })
	retries.attempts = 3
	retries.sleep = func(ctx context.Context, d time.Duration) error { return nil }
	timeout := &url.Error{Op: "Get", URL: "https://gmail.googleapis.com", Err: context.DeadlineExceeded}
	n := 0
	err := retry(context.Background(), func() error {
		n++
		if n < 3 {
			return timeout
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Fatalf("a request timeout must retry: n=%d err=%v", n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n = 0
	if err := retry(ctx, func() error { n++; return timeout }); err == nil || n > 1 {
		t.Fatalf("a canceled sync must not retry: n=%d err=%v", n, err)
	}
}

func TestStatusErrorHidesRawBody(t *testing.T) {
	body := `{"error":{"code":403,"message":"Request had insufficient authentication scopes.","status":"PERMISSION_DENIED","details":[{"secret":"raw-detail-value"}]}}`
	msg := statusErr(403, body, nil).Error()
	if strings.Contains(msg, "raw-detail-value") || !strings.Contains(msg, "insufficient authentication scopes") {
		t.Fatalf("error text %q", msg)
	}
	if msg := statusErr(500, "<html>proxy page with token=abc</html>", nil).Error(); msg != "http 500" {
		t.Fatalf("non-JSON body leaked: %q", msg)
	}
}
