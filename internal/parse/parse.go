package parse

import (
	"encoding/base64"
	"encoding/json"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/store"
)

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
	MimeType string    `json:"mimeType"`
	Headers  []header  `json:"headers"`
	Body     *body     `json:"body"`
	Parts    []payload `json:"parts"`
}

type header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type body struct {
	Data string `json:"data"`
	Size int    `json:"size"`
}

func Message(raw []byte, profileEmail string) (store.Message, error) {
	var g gmailMessage
	if err := json.Unmarshal(raw, &g); err != nil {
		return store.Message{}, err
	}
	msg := store.Message{
		ID:        g.ID,
		ThreadID:  g.ThreadID,
		Snippet:   g.Snippet,
		SizeBytes: g.SizeEstimate,
		LabelIDs:  g.LabelIDs,
		SyncedAt:  time.Now().UTC(),
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
	if g.Payload != nil {
		hdrs := headerMap(g.Payload.Headers)
		msg.FromName, msg.FromEmail = ParseMailbox(hdrs["from"])
		msg.ToEmails = ParseAddressList(hdrs["to"])
		msg.CcEmails = ParseAddressList(hdrs["cc"])
		msg.Subject = hdrs["subject"]
		if body, ok := extractPlain(g.Payload); ok {
			msg.Body = body
			msg.HasBody = true
		}
	}
	msg.IsOutgoing = profileEmail != "" && strings.EqualFold(msg.FromEmail, profileEmail)
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

func headerMap(hs []header) map[string]string {
	out := map[string]string{}
	for _, h := range hs {
		key := strings.ToLower(h.Name)
		if _, ok := out[key]; !ok {
			out[key] = h.Value
		}
	}
	return out
}

func extractPlain(p *payload) (string, bool) {
	if p == nil {
		return "", false
	}
	if strings.EqualFold(p.MimeType, "text/plain") && p.Body != nil && p.Body.Data != "" {
		if s, err := decodeBody(p.Body.Data); err == nil {
			return s, true
		}
	}
	for i := range p.Parts {
		if s, ok := extractPlain(&p.Parts[i]); ok {
			return s, true
		}
	}
	if strings.EqualFold(p.MimeType, "text/html") && p.Body != nil && p.Body.Data != "" {
		if s, err := decodeBody(p.Body.Data); err == nil {
			return s, true
		}
	}
	return "", false
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
