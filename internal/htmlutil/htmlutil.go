package htmlutil

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var (
	colorRe = regexp.MustCompile(`(?i)^(#[0-9a-fA-F]{3,8}|[a-z]+)$`)
	roleRe  = regexp.MustCompile(`(?i)^[a-z][a-z0-9-]*$`)
	policy  = emailPolicy()
)

func LooksLikeHTML(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	low := strings.ToLower(t)
	if strings.HasPrefix(low, "<!doctype html") || strings.HasPrefix(low, "<html") {
		return true
	}
	return strings.HasPrefix(t, "<") && strings.Contains(t, "</")
}

func Sanitize(s string) string {
	return stripRemoteImages(policy.Sanitize(s))
}

func stripRemoteImages(s string) string {
	nodes, err := html.ParseFragment(strings.NewReader(s), &html.Node{
		Type:     html.ElementNode,
		Data:     "div",
		DataAtom: atom.Div,
	})
	if err != nil {
		return s
	}
	var b bytes.Buffer
	for _, n := range nodes {
		walkStripRemoteImages(n)
		if err := html.Render(&b, n); err != nil {
			return s
		}
	}
	return b.String()
}

func walkStripRemoteImages(n *html.Node) {
	if n.Type == html.ElementNode && n.Data == "img" {
		keep := n.Attr[:0]
		for _, a := range n.Attr {
			if remoteImageAttr(a.Key, a.Val) {
				continue
			}
			keep = append(keep, a)
		}
		n.Attr = keep
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkStripRemoteImages(c)
	}
}

func remoteImageAttr(key, val string) bool {
	k := strings.ToLower(key)
	if k != "src" && k != "srcset" {
		return false
	}
	v := strings.TrimSpace(strings.ToLower(val))
	return strings.HasPrefix(v, "http:") || strings.HasPrefix(v, "https:") ||
		strings.HasPrefix(v, "//") || strings.HasPrefix(v, "data:") ||
		strings.Contains(v, "http:") || strings.Contains(v, "https:") || strings.Contains(v, "//")
}

func emailPolicy() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowElements("center", "font")
	p.AllowAttrs("style").Globally()
	p.AllowStyles(
		"background", "background-color", "border", "border-bottom", "border-collapse",
		"border-radius", "color", "display", "font-family", "font-size", "font-weight",
		"height", "line-height", "margin", "max-width", "padding", "text-align",
		"vertical-align", "width",
	).Globally()
	p.AllowAttrs("bgcolor").Matching(colorRe).OnElements("table", "td", "th", "tr", "body")
	p.AllowAttrs("align").Matching(bluemonday.CellAlign).OnElements("table", "div", "p")
	p.AllowAttrs("cellpadding", "cellspacing").Matching(bluemonday.Integer).OnElements("table")
	p.AllowAttrs("border").Matching(bluemonday.Integer).OnElements("table", "img")
	p.AllowAttrs("role").Matching(roleRe).OnElements("table")
	p.AllowAttrs("color", "face", "size").OnElements("font")
	return p
}
