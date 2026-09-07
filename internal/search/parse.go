package search

import (
	"strings"
	"time"
)

type Query struct {
	Text    string
	From    string
	To      string
	Subject string
	Unread  bool
	After   time.Time
	Before  time.Time
}

func Parse(q string) Query {
	var out Query
	var text []string
	for _, tok := range strings.Fields(q) {
		key, val, ok := strings.Cut(tok, ":")
		if !ok {
			if strings.EqualFold(tok, "unread") {
				out.Unread = true
				continue
			}
			text = append(text, tok)
			continue
		}
		switch strings.ToLower(key) {
		case "from":
			if val != "" {
				out.From = val
			}
		case "to":
			if val != "" {
				out.To = val
			}
		case "subject":
			if val != "" {
				out.Subject = val
			}
		case "after":
			if t, err := time.Parse("2006-01-02", val); err == nil {
				out.After = t
			}
		case "before":
			if t, err := time.Parse("2006-01-02", val); err == nil {
				out.Before = t
			}
		case "is":
			if strings.EqualFold(val, "unread") {
				out.Unread = true
			} else {
				text = append(text, tok)
			}
		default:
			text = append(text, tok)
		}
	}
	out.Text = strings.Join(text, " ")
	return out
}
