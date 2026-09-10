package gmail

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBatchURL = "https://www.googleapis.com/batch/gmail/v1"
	maxBatchSize    = 10
	quotaPerGet     = 5
	quotaPerList    = 5
	quotaPerHist    = 2
	quotaPerSec     = 250
	quotaBurst      = 250
	batchGap        = 300 * time.Millisecond
)

var (
	errReplyID        = errors.New("reply id mismatch")
	errDuplicatePart  = errors.New("duplicate batch response")
	errUnexpectedPart = errors.New("unexpected batch response")
	errMissingPart    = errors.New("missing batch response")
)

func encodeBatchGet(ids []string, format string) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for i, id := range ids {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Type", "application/http")
		h.Set("Content-ID", fmt.Sprintf("<%d>", i))
		pw, err := w.CreatePart(h)
		if err != nil {
			return nil, "", err
		}
		q := url.Values{}
		q.Set("format", format)
		path := "/gmail/v1/users/me/messages/" + url.PathEscape(id) + "?" + q.Encode()
		if _, err := fmt.Fprintf(pw, "GET %s HTTP/1.1\r\n\r\n", path); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "multipart/mixed; boundary=" + w.Boundary(), nil
}

func decodeBatch(raw []byte, ids []string) ([]FetchResult, error) {
	body, boundary, err := batchBody(raw)
	if err != nil {
		return nil, err
	}
	return decodeBatchParts(body, boundary, ids)
}

func decodeBatchBody(body []byte, contentType string, ids []string) ([]FetchResult, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, err
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, fmt.Errorf("missing multipart boundary")
	}
	return decodeBatchParts(body, boundary, ids)
}

func decodeBatchParts(body []byte, boundary string, ids []string) ([]FetchResult, error) {
	r := multipart.NewReader(bytes.NewReader(body), boundary)
	slots := make([]*FetchResult, len(ids))
	var extra []FetchResult
	for {
		part, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		p, err := io.ReadAll(part)
		cid := part.Header.Get("Content-ID")
		_ = part.Close()
		if err != nil {
			return nil, err
		}
		resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(p)), nil)
		if err != nil {
			return nil, err
		}
		payload, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		payload = bytes.TrimSpace(payload)
		got := interpretPart(ids, cid, resp.StatusCode, payload, resp.Header)
		idx := parseContentID(cid)
		if idx >= 0 && idx < len(ids) {
			if slots[idx] != nil {
				got.Status = FetchFatal
				got.Err = errDuplicatePart
			}
			slots[idx] = &got
			continue
		}
		if got.Status == FetchOK {
			got.Status = FetchUnexpected
			got.Err = errUnexpectedPart
		}
		extra = append(extra, got)
	}
	out := make([]FetchResult, 0, len(ids)+len(extra))
	for i, id := range ids {
		if slots[i] == nil {
			out = append(out, FetchResult{ID: id, Status: FetchMissing, Err: errMissingPart})
			continue
		}
		res := *slots[i]
		if res.ID == "" {
			res.ID = id
		}
		if res.Status == FetchOK {
			if err := MatchMessageID(res.Raw, id); err != nil {
				res.Status = FetchFatal
				res.Err = err
			}
		}
		out = append(out, res)
	}
	out = append(out, extra...)
	return out, nil
}

func interpretPart(ids []string, contentID string, code int, payload []byte, header http.Header) FetchResult {
	id := ""
	if idx := parseContentID(contentID); idx >= 0 && idx < len(ids) {
		id = ids[idx]
	}
	switch classifyStatus(code, peekReason(string(payload)), string(payload)) {
	case classNotFound:
		return FetchResult{ID: id, Status: FetchNotFound}
	case classRetryable:
		return FetchResult{ID: id, Status: FetchRetryable, Err: statusErr(code, string(payload), header)}
	case classAuth:
		return FetchResult{ID: id, Status: FetchFatal, Err: errors.Join(ErrAuth, statusErr(code, string(payload), header))}
	default:
		if code >= 400 {
			return FetchResult{ID: id, Status: FetchFatal, Err: statusErr(code, string(payload), header)}
		}
		return FetchResult{ID: id, Status: FetchOK, Raw: payload}
	}
}

func parseContentID(cid string) int {
	cid = strings.TrimSpace(cid)
	if !strings.HasPrefix(cid, "<") || !strings.HasSuffix(cid, ">") {
		return -1
	}
	inner := cid[1 : len(cid)-1]
	if inner == "" || strings.ContainsAny(inner, "<>+") {
		return -1
	}
	if rest, ok := strings.CutPrefix(strings.ToLower(inner), "response-"); ok {
		inner = rest
	}
	if inner == "" {
		return -1
	}
	for _, r := range inner {
		if r < '0' || r > '9' {
			return -1
		}
	}
	n, err := strconv.Atoi(inner)
	if err != nil {
		return -1
	}
	return n
}

func jsonMessageID(raw []byte) string {
	var m struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	return m.ID
}

func MatchMessageID(raw []byte, want string) error {
	id := jsonMessageID(raw)
	if id == "" || id != want {
		return fmt.Errorf("%w: got %q", errReplyID, id)
	}
	return nil
}

func batchBody(raw []byte) ([]byte, string, error) {
	if bytes.HasPrefix(raw, []byte("HTTP/")) {
		resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			return nil, "", statusErr(resp.StatusCode, string(bytes.TrimSpace(b)), resp.Header)
		}
		_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if err != nil {
			return nil, "", err
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, "", err
		}
		return body, params["boundary"], nil
	}
	return nil, "", fmt.Errorf("not an http batch response")
}

func splitIDs(ids []string, n int) [][]string {
	if n <= 0 {
		n = maxBatchSize
	}
	var out [][]string
	for len(ids) > 0 {
		k := n
		if k > len(ids) {
			k = len(ids)
		}
		out = append(out, ids[:k])
		ids = ids[k:]
	}
	return out
}
