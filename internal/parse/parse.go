package parse

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

var errEmptyID = fmt.Errorf("empty message id")
var errMissingText = fmt.Errorf("missing referenced text body")
var errBodyDecode = fmt.Errorf("invalid message body encoding")

type gmailMessage struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"threadId"`
	LabelIDs     []string `json:"labelIds"`
	Snippet      string   `json:"snippet"`
	HistoryID    string   `json:"historyId"`
	InternalDate string   `json:"internalDate"`
	SizeEstimate int      `json:"sizeEstimate"`
	Payload      *payload `json:"payload"`
}

type payload struct {
	MimeType string         `json:"mimeType"`
	Filename string         `json:"filename"`
	Headers  []store.Header `json:"headers"`
	Body     *body          `json:"body"`
	Parts    []payload      `json:"parts"`
}

type body struct {
	Data         string `json:"data"`
	Size         int    `json:"size"`
	AttachmentID string `json:"attachmentId"`
}

func Message(raw []byte) (store.Message, error) {
	var g gmailMessage
	if err := json.Unmarshal(raw, &g); err != nil {
		return store.Message{}, err
	}
	if g.ID == "" {
		return store.Message{}, errEmptyID
	}
	msg := store.Message{
		ID:        g.ID,
		ThreadID:  g.ThreadID,
		Snippet:   g.Snippet,
		SizeBytes: g.SizeEstimate,
		LabelIDs:  g.LabelIDs,
		SyncedAt:  time.Now().UTC(),
		Headers:   []store.Header{},
	}
	if g.LabelIDs == nil {
		msg.LabelIDs = []string{}
	}
	if hid, err := strconv.ParseUint(g.HistoryID, 10, 64); err == nil {
		msg.HistoryID = hid
	}
	if ms, err := strconv.ParseInt(g.InternalDate, 10, 64); err == nil {
		msg.InternalDate = time.UnixMilli(ms).UTC()
	}
	msg.IsRead = !hasLabel(g.LabelIDs, "UNREAD")
	msg.IsOutgoing = hasLabel(g.LabelIDs, "SENT")
	if g.Payload != nil {
		if g.Payload.Headers != nil {
			msg.Headers = append([]store.Header{}, g.Payload.Headers...)
		}
		hdrs := headerMap(g.Payload.Headers)
		msg.FromName, msg.FromEmail = ParseMailbox(hdrs["from"])
		msg.ToEmails = ParseAddressList(hdrs["to"])
		msg.CcEmails = ParseAddressList(hdrs["cc"])
		msg.Subject = hdrs["subject"]
		body, err := extractBody(g.Payload)
		if err != nil {
			return store.Message{}, err
		}
		msg.Body = body
		msg.HasBody = body != ""
	}
	return msg, nil
}

func ParseMailbox(s string) (string, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	addr, err := mail.ParseAddress(s)
	if err != nil {
		return "", strings.ToLower(s)
	}
	return addr.Name, strings.ToLower(addr.Address)
}

func ParseAddressList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return []string{}
	}
	list, err := mail.ParseAddressList(s)
	if err != nil {
		_, email := ParseMailbox(s)
		if email == "" {
			return []string{}
		}
		return []string{email}
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, strings.ToLower(a.Address))
	}
	return out
}

func headerMap(hs []store.Header) map[string]string {
	out := map[string]string{}
	for _, h := range hs {
		key := strings.ToLower(h.Name)
		if _, ok := out[key]; !ok {
			out[key] = h.Value
		}
	}
	return out
}

func extractBody(p *payload) (string, error) {
	plain, err := findText(p, "text/plain")
	if err != nil {
		return "", err
	}
	if plain != "" {
		return plain, nil
	}
	return findText(p, "text/html")
}

func findText(p *payload, mime string) (string, error) {
	if p == nil || isAttachment(p) {
		return "", nil
	}
	if strings.EqualFold(p.MimeType, mime) {
		text, err := partText(p)
		if err != nil || text != "" {
			return text, err
		}
	}
	for i := range p.Parts {
		text, err := findText(&p.Parts[i], mime)
		if err != nil || text != "" {
			return text, err
		}
	}
	return "", nil
}

func partText(p *payload) (string, error) {
	if p.Body == nil {
		return "", nil
	}
	if p.Body.Data != "" {
		s, err := decodeBody(p.Body.Data)
		if err != nil {
			return "", fmt.Errorf("%w: %v", errBodyDecode, err)
		}
		return s, nil
	}
	if p.Body.AttachmentID != "" || p.Body.Size > 0 {
		return "", errMissingText
	}
	return "", nil
}

func isAttachment(p *payload) bool {
	if strings.EqualFold(p.MimeType, "message/rfc822") {
		return true
	}
	if p.Filename != "" {
		return true
	}
	if disp := headerValue(p.Headers, "content-disposition"); strings.Contains(strings.ToLower(disp), "attachment") {
		return true
	}
	if p.Body != nil && p.Body.AttachmentID != "" && !isTextMIME(p.MimeType) {
		return true
	}
	return false
}

func isTextMIME(mt string) bool {
	return strings.EqualFold(mt, "text/plain") || strings.EqualFold(mt, "text/html")
}

func headerValue(hs []store.Header, name string) string {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

func decodeBody(data string) (string, error) {
	data = strings.ReplaceAll(data, "-", "+")
	data = strings.ReplaceAll(data, "_", "/")
	b, err := base64.StdEncoding.DecodeString(padB64(data))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func padB64(s string) string {
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	return s
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}
