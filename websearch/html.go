package websearch

import (
	"net/url"
	"strings"

	"github.com/snowmerak/q/webbrowser"
	"golang.org/x/net/html"
)

func extractDocument(doc *html.Node, base *url.URL) webbrowser.PageContent {
	p := webbrowser.PageContent{URL: base.String(), Links: []webbrowser.Link{}}
	root, priority, articles := doc, 0, 0
	var inspect func(*html.Node)
	inspect = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if inertElement(n.Data) {
				return
			}
			if n.Data == "title" {
				p.Title = clip(nodeText(n), 200)
			}
			if n.Data == "article" {
				articles++
			}
			if score := contentPriority(n); score > priority {
				root, priority = n, score
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			inspect(c)
		}
	}
	inspect(doc)
	if priority == 70 && articles > 1 {
		root = doc
	}
	seen := map[string]bool{}
	var links func(*html.Node)
	links = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if inertElement(n.Data) {
				return
			}
			if n.Data == "a" && len(p.Links) < 300 {
				u, err := url.Parse(attribute(n, "href"))
				if err == nil && attribute(n, "href") != "" {
					u = base.ResolveReference(u)
					if v := u.Query().Get("uddg"); v != "" && (u.Hostname() == "duckduckgo.com" || strings.HasSuffix(u.Hostname(), ".duckduckgo.com")) {
						if destination, err := url.Parse(v); err == nil {
							u = destination
						}
					}
					u.Fragment = ""
					key := pageURL(u.String())
					if validURL(u.String()) && !seen[key] {
						label := nodeText(n)
						if label == "" {
							label = attribute(n, "title")
						}
						p.Links = append(p.Links, webbrowser.Link{Text: clip(label, 300), URL: u.String()})
						seen[key] = true
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			links(c)
		}
	}
	// Prioritize article links, then retain navigation links for further research.
	// Excluding navigation prose must not discard its destination URLs.
	links(root)
	if root != doc {
		links(doc)
	}
	var text strings.Builder
	var body func(*html.Node)
	body = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if inertElement(n.Data) || n.Data == "nav" || n.Data == "footer" || n.Data == "title" || (root == doc && n.Data == "header") {
				return
			}
			if n.Data == "p" || n.Data == "div" || n.Data == "li" || n.Data == "h1" || n.Data == "h2" || n.Data == "h3" || n.Data == "pre" || n.Data == "br" {
				text.WriteByte('\n')
			}
			if n.Data == "img" {
				if alt := strings.TrimSpace(attribute(n, "alt")); alt != "" {
					text.WriteByte('\n')
					text.WriteString(alt)
					text.WriteByte('\n')
				}
			}
		}
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
			text.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			body(c)
		}
	}
	body(root)
	var lines []string
	for _, line := range strings.Split(text.String(), "\n") {
		if normalized := strings.Join(strings.Fields(line), " "); normalized != "" {
			lines = append(lines, normalized)
		}
	}
	raw := strings.Join(lines, "\n\n")
	p.Text = clip(raw, 250000)
	p.Truncated = len(raw) > len(p.Text)
	return p
}

func attribute(n *html.Node, name string) string {
	for _, attr := range n.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}
	return ""
}

func nodeText(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var text strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		text.WriteString(nodeText(c))
		text.WriteByte(' ')
	}
	return strings.Join(strings.Fields(text.String()), " ")
}

func inertElement(tag string) bool {
	switch tag {
	case "script", "style", "noscript", "svg", "template":
		return true
	}
	return false
}

func contentPriority(n *html.Node) int {
	if n.Data == "main" {
		return 100
	}
	if attribute(n, "role") == "main" {
		return 90
	}
	id := strings.ToLower(attribute(n, "id"))
	if id == "content" || id == "contents" || strings.HasSuffix(id, "main_content") || id == "main-content" {
		return 80
	}
	if n.Data == "article" {
		return 70
	}
	return 0
}
