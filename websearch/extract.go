package websearch

import (
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/snowmerak/q/webbrowser"
)

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_' })
}
func relevance(text, goal string) int {
	score := 0
	lower := strings.ToLower(text)
	seen := map[string]bool{}
	for _, w := range words(goal) {
		if len(w) > 2 && !seen[w] {
			seen[w] = true
			if strings.Contains(lower, w) {
				score++
			}
		}
	}
	return score
}

// Keep paragraph context and overlapping windows, including qualifiers. Rank
// rather than discard low scoring passages; they remain available next step.
func chunks(text, goal string) []string {
	text = strings.TrimSpace(text)
	var result []string
	for text != "" && len(result) < 220 {
		part := clip(text, 1400)
		result = append(result, part)
		if len(part) == len(text) {
			break
		}
		n := len(part) - 250
		for n > 0 && !utf8.RuneStart(text[n]) {
			n--
		}
		text = strings.TrimSpace(text[n:])
	}
	sort.SliceStable(result, func(i, j int) bool { return relevance(result[i], goal) > relevance(result[j], goal) })
	return result
}

func rankedLinks(links []webbrowser.Link, goal string) []webbrowser.Link {
	out := make([]webbrowser.Link, 0, len(links))
	seen := map[string]bool{}
	for _, l := range links {
		if u, err := url.Parse(l.URL); err == nil {
			host := strings.ToLower(u.Hostname())
			if host == "duckduckgo.com" || strings.HasSuffix(host, ".duckduckgo.com") {
				if target := u.Query().Get("uddg"); validURL(target) {
					l.URL = target
				}
			}
		}
		if !seen[l.URL] && len(l.URL) <= 2000 {
			seen[l.URL] = true
			out = append(out, l)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return relevance(out[i].Text+" "+out[i].URL, goal) > relevance(out[j].Text+" "+out[j].URL, goal)
	})
	return out
}
