package gmail

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
)

// decodeBatch decodes a whole HTTP batch reply, as the tests store them.
// Production code reads the reply with net/http and calls decodeBatchBody.
func decodeBatch(raw []byte, ids []string) ([]FetchResult, error) {
	body, boundary, err := batchBody(raw)
	if err != nil {
		return nil, err
	}
	return decodeBatchParts(bytes.NewReader(body), boundary, ids)
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
