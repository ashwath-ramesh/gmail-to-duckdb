package gmail

import "fmt"

type FetchStatus int

const (
	FetchOK FetchStatus = iota
	FetchNotFound
	FetchRetryable
	FetchFatal
	FetchMissing
	FetchUnexpected
)

func (s FetchStatus) String() string {
	switch s {
	case FetchOK:
		return "ok"
	case FetchNotFound:
		return "not_found"
	case FetchRetryable:
		return "retryable"
	case FetchFatal:
		return "fatal"
	case FetchMissing:
		return "missing"
	case FetchUnexpected:
		return "unexpected"
	default:
		return fmt.Sprintf("status_%d", int(s))
	}
}

// FetchResult is one requested message GET, aligned to the requested ID.
type FetchResult struct {
	ID     string
	Status FetchStatus
	Raw    []byte
	Err    error
}

func (r FetchResult) OK() bool { return r.Status == FetchOK }

func (r FetchResult) Deleted() bool { return r.Status == FetchNotFound }

func (r FetchResult) Unresolved() bool {
	switch r.Status {
	case FetchOK, FetchNotFound:
		return false
	default:
		return true
	}
}
