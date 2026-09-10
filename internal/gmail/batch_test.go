package gmail

import (
	"strconv"
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
	if strings.Contains(s, "metadataHeaders") {
		t.Fatalf("metadata must request all headers: %s", s)
	}
	if !strings.Contains(s, "GET /gmail/v1/users/me/messages/bbb?") {
		t.Fatalf("missing bbb: %s", s)
	}
	if !strings.Contains(strings.ToLower(s), "content-id: <0>") || !strings.Contains(strings.ToLower(s), "content-id: <1>") {
		t.Fatalf("content-id: %s", s)
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

func TestDecodeBatchMapsContentID(t *testing.T) {
	raw := batchHTTP(
		part("response-1", 200, `{"id":"bbb"}`),
		part("response-0", 200, `{"id":"aaa"}`),
	)
	parts, err := decodeBatch(raw, []string{"aaa", "bbb"})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("len=%d", len(parts))
	}
	if parts[0].ID != "aaa" || parts[0].Status != FetchOK || string(parts[0].Raw) != `{"id":"aaa"}` {
		t.Fatalf("aaa %+v", parts[0])
	}
	if parts[1].ID != "bbb" || parts[1].Status != FetchOK || string(parts[1].Raw) != `{"id":"bbb"}` {
		t.Fatalf("bbb %+v", parts[1])
	}
}

func TestDecodeBatch404IsNotFound(t *testing.T) {
	raw := batchHTTP(
		part("response-0", 200, `{"id":"aaa"}`),
		part("response-1", 404, `{"error":{"code":404,"message":"Requested entity was not found."}}`),
	)
	parts, err := decodeBatch(raw, []string{"aaa", "gone"})
	if err != nil {
		t.Fatal(err)
	}
	if parts[0].Status != FetchOK || parts[0].ID != "aaa" {
		t.Fatalf("aaa %+v", parts[0])
	}
	if parts[1].ID != "gone" || parts[1].Status != FetchNotFound {
		t.Fatalf("404 must be deletion, not missing: %+v", parts[1])
	}
	if parts[1].Status == FetchMissing {
		t.Fatal("404 confused with missing response")
	}
}

func TestDecodeBatchMissingPart(t *testing.T) {
	raw := batchHTTP(part("response-0", 200, `{"id":"aaa"}`))
	parts, err := decodeBatch(raw, []string{"aaa", "bbb"})
	if err != nil {
		t.Fatal(err)
	}
	if parts[0].Status != FetchOK {
		t.Fatalf("aaa %+v", parts[0])
	}
	if parts[1].ID != "bbb" || parts[1].Status != FetchMissing {
		t.Fatalf("missing %+v", parts[1])
	}
}

func TestDecodeBatchDuplicateAndUnexpected(t *testing.T) {
	raw := batchHTTP(
		part("response-0", 200, `{"id":"aaa"}`),
		part("response-0", 200, `{"id":"aaa"}`),
		part("response-9", 200, `{"id":"zzz"}`),
	)
	parts, err := decodeBatch(raw, []string{"aaa"})
	if err != nil {
		t.Fatal(err)
	}
	if parts[0].ID != "aaa" || parts[0].Status != FetchFatal {
		t.Fatalf("duplicate first slot %+v", parts[0])
	}
	var sawUnexpected bool
	for _, p := range parts[1:] {
		if p.Status == FetchUnexpected || p.ID == "zzz" {
			sawUnexpected = true
		}
	}
	if !sawUnexpected {
		t.Fatalf("unexpected: %+v", parts)
	}
}

func TestDecodeBatchReplyIDMismatch(t *testing.T) {
	raw := batchHTTP(part("response-0", 200, `{"id":"other"}`))
	parts, err := decodeBatch(raw, []string{"aaa"})
	if err != nil {
		t.Fatal(err)
	}
	if parts[0].ID != "aaa" || parts[0].Status != FetchFatal || parts[0].Err == nil {
		t.Fatalf("mismatch %+v", parts[0])
	}
}

func TestDecodeBatchHTTPError(t *testing.T) {
	raw := batchHTTP(part("response-0", 500, `{"error":{"code":500}}`))
	parts, err := decodeBatch(raw, []string{"aaa"})
	if err != nil {
		t.Fatal(err)
	}
	if parts[0].Status != FetchRetryable {
		t.Fatalf("500 %+v", parts[0])
	}
}

func TestDecodeBatch429(t *testing.T) {
	raw := batchHTTP(part("response-0", 429, `{"error":{"code":429}}`))
	parts, err := decodeBatch(raw, []string{"aaa"})
	if err != nil {
		t.Fatal(err)
	}
	if parts[0].Status != FetchRetryable || !isRetryable(parts[0].Err) {
		t.Fatalf("429 %+v", parts[0])
	}
}

func TestDecodeBatchEmptyIDIsNotOK(t *testing.T) {
	raw := batchHTTP(part("response-0", 200, `{"threadId":"t"}`))
	parts, err := decodeBatch(raw, []string{"aaa"})
	if err != nil {
		t.Fatal(err)
	}
	if parts[0].Status == FetchOK {
		t.Fatalf("empty id must not be OK: %+v", parts[0])
	}
}

func TestDecodeBatchMalformedJSONIsNotOK(t *testing.T) {
	raw := batchHTTP(part("response-0", 200, `{`))
	parts, err := decodeBatch(raw, []string{"aaa"})
	if err != nil {
		t.Fatal(err)
	}
	if parts[0].Status == FetchOK {
		t.Fatalf("malformed json must not be OK: %+v", parts[0])
	}
}

func TestDecodeBatchRejectsPayloadFallback(t *testing.T) {
	raw := batchHTTP(
		"--batch_b\r\n" +
			"Content-Type: application/http\r\n" +
			"\r\n" +
			"HTTP/1.1 200 OK\r\n" +
			"Content-Type: application/json\r\n" +
			"\r\n" +
			`{"id":"aaa"}` + "\r\n",
	)
	parts, err := decodeBatch(raw, []string{"aaa"})
	if err != nil {
		t.Fatal(err)
	}
	if parts[0].ID != "aaa" || parts[0].Status != FetchMissing {
		t.Fatalf("no content-id must not trust payload: %+v", parts[0])
	}
}

func TestDecodeBatchRejectsMalformedContentID(t *testing.T) {
	raw := batchHTTP(part("not-an-index", 200, `{"id":"aaa"}`))
	parts, err := decodeBatch(raw, []string{"aaa"})
	if err != nil {
		t.Fatal(err)
	}
	if parts[0].Status != FetchMissing {
		t.Fatalf("malformed content-id must not map: %+v", parts)
	}
}

func TestDecodeBatchRejectsNestedAndSignedContentID(t *testing.T) {
	for _, cid := range []string{"<0>>", "+0"} {
		raw := batchHTTP(part(cid, 200, `{"id":"aaa"}`))
		parts, err := decodeBatch(raw, []string{"aaa"})
		if err != nil {
			t.Fatal(err)
		}
		if parts[0].Status != FetchMissing || parts[0].ID != "aaa" {
			t.Fatalf("%q correlated: %+v", cid, parts[0])
		}
	}
	ok, err := decodeBatch(batchHTTP(part("0", 200, `{"id":"aaa"}`)), []string{"aaa"})
	if err != nil {
		t.Fatal(err)
	}
	if ok[0].Status != FetchOK || ok[0].ID != "aaa" {
		t.Fatalf("plain index %+v", ok[0])
	}
	resp, err := decodeBatch(batchHTTP(part("response-0", 200, `{"id":"aaa"}`)), []string{"aaa"})
	if err != nil {
		t.Fatal(err)
	}
	if resp[0].Status != FetchOK || resp[0].ID != "aaa" {
		t.Fatalf("response-N %+v", resp[0])
	}
}

func TestQuotaFitsBurst(t *testing.T) {
	if quotaPerGet*maxBatchSize > quotaBurst {
		t.Fatalf("batch %d gets need %d units; burst is %d", maxBatchSize, quotaPerGet*maxBatchSize, quotaBurst)
	}
}

func batchHTTP(parts ...string) []byte {
	var b strings.Builder
	b.WriteString("HTTP/1.1 200 OK\r\nContent-Type: multipart/mixed; boundary=batch_b\r\n\r\n")
	for _, p := range parts {
		b.WriteString(p)
	}
	b.WriteString("--batch_b--\r\n")
	return []byte(b.String())
}

func part(cid string, code int, body string) string {
	status := "OK"
	if code == 404 {
		status = "Not Found"
	}
	if code == 429 {
		status = "Too Many Requests"
	}
	if code == 500 {
		status = "Internal Server Error"
	}
	if code == 503 {
		status = "Service Unavailable"
	}
	return "--batch_b\r\n" +
		"Content-Type: application/http\r\n" +
		"Content-ID: <" + cid + ">\r\n" +
		"\r\n" +
		"HTTP/1.1 " + strconv.Itoa(code) + " " + status + "\r\n" +
		"Content-Type: application/json\r\n" +
		"\r\n" +
		body + "\r\n"
}
