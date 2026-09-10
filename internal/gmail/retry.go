package gmail

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
)

var (
	ErrHistoryGone      = errors.New("gmail history id expired")
	ErrPageTokenExpired = errors.New("gmail page token expired")
	ErrAuth             = errors.New("gmail auth or permission failed")
)

type errorClass int

const (
	classFatal errorClass = iota
	classNotFound
	classRetryable
	classAuth
	classPageToken
)

type retrySettings struct {
	attempts int
	initial  time.Duration
	max      time.Duration
	sleep    func(context.Context, time.Duration) error
}

var retries = retrySettings{
	attempts: 8,
	initial:  2 * time.Second,
	max:      30 * time.Second,
	sleep:    sleepContext,
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func retry(ctx context.Context, fn func() error) error {
	var err error
	backoff := retries.initial
	for attempt := 0; attempt < retries.attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err = fn()
		if err == nil {
			return nil
		}
		if !isRetryable(err) {
			return err
		}
		if attempt == retries.attempts-1 {
			return err
		}
		wait := retryAfterOf(err)
		if wait <= 0 {
			wait = backoff
		}
		if err := retries.sleep(ctx, wait); err != nil {
			return err
		}
		backoff *= 2
		if backoff > retries.max {
			backoff = retries.max
		}
	}
	return err
}

func isRetryable(err error) bool {
	return classifyError(err) == classRetryable
}

func classifyError(err error) errorClass {
	if err == nil {
		return classFatal
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return classFatal
	}
	if errors.Is(err, ErrPageTokenExpired) {
		return classPageToken
	}
	if errors.Is(err, ErrAuth) {
		return classAuth
	}
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return classAuth
	}
	var se *statusError
	if errors.As(err, &se) {
		return classifyStatus(se.Code, se.Reason, se.Body)
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		return classifyStatus(gerr.Code, quotaReason(gerr), gerr.Message+" "+gerr.Body)
	}
	if transientNet(err) {
		return classRetryable
	}
	return classFatal
}

func classifyStatus(code int, reason, body string) errorClass {
	switch code {
	case http.StatusNotFound:
		return classNotFound
	case http.StatusUnauthorized:
		return classAuth
	case http.StatusForbidden:
		if quotaHint(reason, body) {
			return classRetryable
		}
		return classAuth
	case http.StatusTooManyRequests:
		return classRetryable
	case http.StatusBadRequest:
		if pageTokenHint(reason, body) {
			return classPageToken
		}
		return classFatal
	default:
		if code >= 500 && code <= 599 {
			return classRetryable
		}
		return classFatal
	}
}

func quotaReason(gerr *googleapi.Error) string {
	for _, e := range gerr.Errors {
		if quotaHint(e.Reason, e.Message) {
			return e.Reason
		}
	}
	return ""
}

func quotaHint(reason, body string) bool {
	s := strings.ToLower(reason + " " + body)
	return strings.Contains(s, "ratelimitexceeded") ||
		strings.Contains(s, "userratelimitexceeded") ||
		strings.Contains(s, "quotaexceeded") ||
		strings.Contains(s, "dailylimitexceeded") ||
		strings.Contains(s, "resource_exhausted")
}

func pageTokenHint(reason, body string) bool {
	s := strings.ToLower(reason + " " + body)
	return strings.Contains(s, "page token") ||
		strings.Contains(s, "pagetoken") ||
		strings.Contains(s, "invalidvalue") && strings.Contains(s, "page")
}

func transientNet(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return false
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return true
		}
		if ue.Err != nil {
			return transientNet(ue.Err)
		}
		return false
	}
	var ne net.Error
	if !errors.As(err, &ne) {
		return false
	}
	if ne.Timeout() {
		return true
	}
	s := strings.ToLower(ne.Error())
	return strings.Contains(s, "connection reset") || strings.Contains(s, "broken pipe")
}

func retryAfterOf(err error) time.Duration {
	var se *statusError
	if errors.As(err, &se) {
		return parseRetryAfter(se.Header)
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		return parseRetryAfter(gerr.Header)
	}
	return 0
}

func parseRetryAfter(h http.Header) time.Duration {
	if h == nil {
		return 0
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if when, err := http.ParseTime(v); err == nil {
		d := time.Until(when)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}

func mapAPIError(err error) error {
	switch classifyError(err) {
	case classAuth:
		if errors.Is(err, ErrAuth) {
			return err
		}
		return errors.Join(ErrAuth, err)
	case classPageToken:
		return ErrPageTokenExpired
	default:
		return err
	}
}

type statusError struct {
	Code   int
	Reason string
	Body   string
	Header http.Header
}

func (e *statusError) Error() string {
	if e.Reason != "" {
		return "http " + strconv.Itoa(e.Code) + ": " + e.Reason + ": " + e.Body
	}
	return "http " + strconv.Itoa(e.Code) + ": " + e.Body
}

func statusErr(code int, body string, header http.Header) error {
	h := http.Header{}
	if header != nil {
		h = header.Clone()
	}
	return &statusError{Code: code, Body: strings.TrimSpace(body), Header: h, Reason: peekReason(body)}
}

func peekReason(body string) string {
	low := strings.ToLower(body)
	for _, r := range []string{"rateLimitExceeded", "userRateLimitExceeded", "quotaExceeded", "dailyLimitExceeded"} {
		if strings.Contains(low, strings.ToLower(r)) {
			return r
		}
	}
	return ""
}
