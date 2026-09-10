package search

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxQueryRunes = 200

type Query struct {
	Terms   []string
	From    string
	To      string
	Subject string
	Unread  bool
	After   time.Time
	Before  time.Time
}

type Error struct {
	msg string
}

func (e *Error) Error() string { return e.msg }

func IsValidation(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

func validation(msg string) error {
	return &Error{msg: msg}
}

func Parse(q string) (Query, error) {
	if utf8.RuneCountInString(q) > MaxQueryRunes {
		return Query{}, validation("query exceeds 200 characters")
	}
	toks, err := scan(q)
	if err != nil {
		return Query{}, err
	}
	var out Query
	seen := map[string]bool{}
	set := func(name, val string) error {
		if strings.TrimSpace(val) == "" {
			return validation("empty " + name + " filter")
		}
		if seen[name] {
			return validation("repeated " + name + " filter")
		}
		seen[name] = true
		switch name {
		case "from":
			out.From = val
		case "to":
			out.To = val
		case "subject":
			out.Subject = val
		}
		return nil
	}
	setUnread := func() error {
		if seen["unread"] {
			return validation("repeated unread filter")
		}
		seen["unread"] = true
		out.Unread = true
		return nil
	}
	setDate := func(name, val string) error {
		if val == "" {
			return validation("empty " + name + " filter")
		}
		if seen[name] {
			return validation("repeated " + name + " filter")
		}
		t, err := time.Parse("2006-01-02", val)
		if err != nil {
			return validation("invalid " + name + " date")
		}
		seen[name] = true
		if name == "after" {
			out.After = t
		} else {
			out.Before = t
		}
		return nil
	}
	for _, tok := range toks {
		if tok.quoted {
			if strings.TrimSpace(tok.val) == "" {
				return Query{}, validation("empty quote")
			}
			out.Terms = append(out.Terms, tok.val)
			continue
		}
		if !strings.Contains(tok.val, ":") {
			if strings.EqualFold(tok.val, "unread") {
				if err := setUnread(); err != nil {
					return Query{}, err
				}
				continue
			}
			out.Terms = append(out.Terms, tok.val)
			continue
		}
		key, val, _ := strings.Cut(tok.val, ":")
		switch strings.ToLower(key) {
		case "from", "to", "subject":
			if err := set(strings.ToLower(key), val); err != nil {
				return Query{}, err
			}
		case "after":
			if err := setDate("after", val); err != nil {
				return Query{}, err
			}
		case "before":
			if err := setDate("before", val); err != nil {
				return Query{}, err
			}
		case "is":
			if val == "" {
				return Query{}, validation("empty is filter")
			}
			if !strings.EqualFold(val, "unread") {
				return Query{}, validation(fmt.Sprintf("unsupported operator %q", key+":"+val))
			}
			if err := setUnread(); err != nil {
				return Query{}, err
			}
		default:
			return Query{}, validation(fmt.Sprintf("unsupported operator %q", key))
		}
	}
	if !out.After.IsZero() && !out.Before.IsZero() && !out.Before.After(out.After) {
		return Query{}, validation("before date must be later than after date")
	}
	return out, nil
}

type token struct {
	val    string
	quoted bool
}

func scan(q string) ([]token, error) {
	rs := []rune(q)
	var out []token
	i := 0
	for i < len(rs) {
		if unicode.IsSpace(rs[i]) {
			i++
			continue
		}
		if rs[i] == '"' {
			val, next, err := readQuoted(rs, i)
			if err != nil {
				return nil, err
			}
			out = append(out, token{val: val, quoted: true})
			i = next
			continue
		}
		start := i
		for i < len(rs) && !unicode.IsSpace(rs[i]) {
			if rs[i] == '"' {
				return nil, validation("unmatched quote")
			}
			if rs[i] == ':' && i+1 < len(rs) && rs[i+1] == '"' {
				prefix := string(rs[start : i+1])
				val, next, err := readQuoted(rs, i+1)
				if err != nil {
					return nil, err
				}
				out = append(out, token{val: prefix + val})
				i = next
				start = -1
				break
			}
			i++
		}
		if start >= 0 {
			out = append(out, token{val: string(rs[start:i])})
		}
	}
	return out, nil
}

func readQuoted(rs []rune, i int) (string, int, error) {
	if i >= len(rs) || rs[i] != '"' {
		return "", 0, validation("unmatched quote")
	}
	i++
	var b strings.Builder
	for i < len(rs) {
		if rs[i] == '\\' {
			if i+1 >= len(rs) {
				return "", 0, validation("unmatched quote")
			}
			next := rs[i+1]
			if next == '"' || next == '\\' {
				b.WriteRune(next)
				i += 2
				continue
			}
			b.WriteRune(rs[i])
			i++
			continue
		}
		if rs[i] == '"' {
			if i+1 < len(rs) && !unicode.IsSpace(rs[i+1]) {
				return "", 0, validation("unmatched quote")
			}
			return b.String(), i + 1, nil
		}
		b.WriteRune(rs[i])
		i++
	}
	return "", 0, validation("unmatched quote")
}
