package gmail

import (
	"strings"
	"testing"
)

func TestEncodeBatchGet(t *testing.T) {
	body, contentType, err := encodeBatchGet([]string{"aaa", "bbb"}, "metadata")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(contentType, "multipart/mixed; boundary=") {
		t.Fatalf("ct: %s", contentType)
	}
	s := string(body)
	if !strings.Contains(s, "GET /gmail/v1/users/me/messages/aaa?format=metadata") {
		t.Fatalf("missing aaa: %s", s)
	}
	if !strings.Contains(s, "metadataHeaders=From") {
		t.Fatalf("missing headers: %s", s)
	}
	if !strings.Contains(s, "GET /gmail/v1/users/me/messages/bbb?") {
		t.Fatalf("missing bbb: %s", s)
	}
	if strings.Contains(s, "format=metadata") && strings.Count(s, "GET ") != 2 {
		t.Fatalf("want 2 gets: %s", s)
	}
}

func TestEncodeBatchFullOmitsMetadataHeaders(t *testing.T) {
	body, _, err := encodeBatchGet([]string{"x"}, "full")
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if !strings.Contains(s, "format=full") {
		t.Fatalf("missing full: %s", s)
	}
	if strings.Contains(s, "metadataHeaders") {
		t.Fatalf("full should omit metadataHeaders: %s", s)
	}
}

func TestDecodeBatch(t *testing.T) {
	raw := []byte(
		"HTTP/1.1 200 OK\r\n" +
			"Content-Type: multipart/mixed; boundary=batch_b\r\n" +
			"\r\n" +
			"--batch_b\r\n" +
			"Content-Type: application/http\r\n" +
			"Content-ID: <response-0>\r\n" +
			"\r\n" +
			"HTTP/1.1 200 OK\r\n" +
			"Content-Type: application/json; charset=UTF-8\r\n" +
			"\r\n" +
			"{\"id\":\"aaa\"}\r\n" +
			"--batch_b\r\n" +
			"Content-Type: application/http\r\n" +
			"Content-ID: <response-1>\r\n" +
			"\r\n" +
			"HTTP/1.1 200 OK\r\n" +
			"Content-Type: application/json; charset=UTF-8\r\n" +
			"\r\n" +
			"{\"id\":\"bbb\"}\r\n" +
			"--batch_b--\r\n",
	)
	parts, err := decodeBatch(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("len=%d", len(parts))
	}
	if string(parts[0]) != `{"id":"aaa"}` || string(parts[1]) != `{"id":"bbb"}` {
		t.Fatalf("parts=%q %q", parts[0], parts[1])
	}
}

func TestQuotaFitsBurst(t *testing.T) {
	if quotaPerGet*maxBatchSize > quotaBurst {
		t.Fatalf("batch %d gets need %d units; burst is %d", maxBatchSize, quotaPerGet*maxBatchSize, quotaBurst)
	}
}

func TestDecodeBatchHTTPError(t *testing.T) {
	raw := []byte(
		"HTTP/1.1 200 OK\r\n" +
			"Content-Type: multipart/mixed; boundary=batch_b\r\n" +
			"\r\n" +
			"--batch_b\r\n" +
			"Content-Type: application/http\r\n" +
			"\r\n" +
			"HTTP/1.1 500 Internal Server Error\r\n" +
			"Content-Type: application/json\r\n" +
			"\r\n" +
			"{\"error\":{\"code\":500}}\r\n" +
			"--batch_b--\r\n",
	)
	_, err := decodeBatch(raw)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeBatch429(t *testing.T) {
	raw := []byte(
		"HTTP/1.1 200 OK\r\n" +
			"Content-Type: multipart/mixed; boundary=batch_b\r\n" +
			"\r\n" +
			"--batch_b\r\n" +
			"Content-Type: application/http\r\n" +
			"\r\n" +
			"HTTP/1.1 429 Too Many Requests\r\n" +
			"Content-Type: application/json\r\n" +
			"\r\n" +
			"{\"error\":{\"code\":429}}\r\n" +
			"--batch_b--\r\n",
	)
	err := mustDecode429(t, raw)
	if !retryable(err) {
		t.Fatalf("inner 429 must be retryable: %v", err)
	}
}

func mustDecode429(t *testing.T, raw []byte) error {
	t.Helper()
	_, err := decodeBatch(raw)
	if err == nil {
		t.Fatal("expected error")
	}
	return err
}
