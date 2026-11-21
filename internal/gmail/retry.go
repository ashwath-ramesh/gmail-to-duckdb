package gmail

import (
	"context"
	"encoding/json"
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
		if !isRetryable(err) && !requestTimeout(ctx, err) {
			return err
		}
		if attempt == retries.attempts-1 {
			return err
		}
		wait := retryAfterOf(err)
		if wait <= 0 {
			wait = backoff
		} else if wait > maxRetryAfter {
			wait = maxRetryAfter
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

// maxRetryAfter caps a server Retry-After value, so that one bad reply
// cannot stop sync for hours.
const maxRetryAfter = 5 * time.Minute

// requestTimeout reports an HTTP client timeout while the caller's context is
// still alive. Go reports it as context.DeadlineExceeded, but a new attempt can work.
func requestTimeout(ctx context.Context, err error) bool {
	var ue *url.Error
	return ctx.Err() == nil && errors.As(err, &ue) && ue.Timeout()
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

// Error never includes the raw reply body: it can hold request details, and
// sync stores the error text and shows it in the UI.
func (e *statusError) Error() string {
	s := "http " + strconv.Itoa(e.Code)
	if e.Reason != "" {
		s += ": " + e.Reason
	}
	if msg := googleMessage(e.Body); msg != "" {
		s += ": " + msg
	}
	return s
}

// googleMessage returns error.message from a Google JSON error reply, cut to
// 200 characters, or "" if the body is not that shape.
func googleMessage(body string) string {
	var reply struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &reply) != nil {
		return ""
	}
	msg := []rune(strings.TrimSpace(reply.Error.Message))
	if len(msg) > 200 {
		msg = append(msg[:200], '…')
	}
	return string(msg)
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
