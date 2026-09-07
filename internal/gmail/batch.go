package gmail

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
)

const (
	batchURL     = "https://www.googleapis.com/batch/gmail/v1"
	maxBatchSize = 10
	quotaPerGet  = 5
	quotaPerList = 5
	quotaPerHist = 2
	quotaPerSec  = 250
	quotaBurst   = 250
)

var metadataHeaders = []string{"From", "To", "Cc", "Bcc", "Subject", "Date"}

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
		if format == "metadata" {
			for _, name := range metadataHeaders {
				q.Add("metadataHeaders", name)
			}
		}
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

func decodeBatch(raw []byte) ([][]byte, error) {
	body, boundary, err := batchBody(raw)
	if err != nil {
		return nil, err
	}
	r := multipart.NewReader(bytes.NewReader(body), boundary)
	var out [][]byte
	for {
		part, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		p, err := io.ReadAll(part)
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
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return nil, fmt.Errorf("batch part %s: %s", resp.Status, payload)
		}
		if resp.StatusCode >= 400 {
			continue
		}
		out = append(out, payload)
	}
	return out, nil
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
			return nil, "", fmt.Errorf("batch http %s: %s", resp.Status, bytes.TrimSpace(b))
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

func decodeBatchBody(body []byte, contentType string) ([][]byte, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, err
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, fmt.Errorf("missing multipart boundary")
	}
	var buf bytes.Buffer
	buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: ")
	buf.WriteString(contentType)
	buf.WriteString("\r\n\r\n")
	buf.Write(body)
	return decodeBatch(buf.Bytes())
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
