package websearch

import (
	"net/url"
	"strings"
)

// Known search result pages expose destinations, not original source evidence.
// This structural distinction does not require a model call.
func searchResults(target string) bool {
	u, err := url.Parse(target)
	if err != nil || u.Query().Get("q") == "" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "html.duckduckgo.com", "duckduckgo.com", "www.duckduckgo.com", "lite.duckduckgo.com":
		return true
	case "www.google.com", "google.com", "www.bing.com", "bing.com":
		return u.Path == "/search"
	}
	return false
}

func searchChallenge(p Document) bool {
	u, err := url.Parse(p.URL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "duckduckgo.com" && !strings.HasSuffix(host, ".duckduckgo.com") {
		return false
	}
	text := strings.ToLower(p.Text)
	return strings.Contains(text, "bots use duckduckgo too") && strings.Contains(text, "challenge")
}
