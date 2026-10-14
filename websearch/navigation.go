package websearch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

func contentHash(p Document) string {
	// Transport markup, refs and tracking IDs are not new source content.
	text := strings.Join(strings.Fields(p.Text), " ")
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func (r *run) actionKey(a action) string {
	value := a.value
	if a.kind == "open" || a.kind == "back" {
		value = pageURL(value)
	}
	key := pageURL(r.page.URL)
	return key + "\n" + r.hashes[key] + "\n" + a.kind + "\n" + a.identity + "\n" + value
}

func passageKey(target, text string) string {
	return pageURL(target) + "\n" + strings.Join(strings.Fields(text), " ")
}

func (r *run) load(ctx context.Context, target string, render bool) bool {
	key := pageURL(target)
	r.visited[key] = true
	p, err := r.session.Page(ctx, target, render)
	if err != nil {
		r.fail(err)
		r.lastOutcome = "failed"
		r.blockedURLs[key] = true
		r.page = Document{}
		r.page.URL = target
		r.pageAccepted = nil
		return false
	}
	changed := r.update(p, nil)
	if !validURL(p.URL) {
		return false
	}
	if !changed {
		r.blockedURLs[key] = true
	} else {
		delete(r.blockedURLs, key)
	}
	// Remember both the requested URL and its redirect destination.
	r.hashes[key] = r.hashes[pageURL(p.URL)]
	r.pageBodies[key] = r.page.Text
	return changed
}

func (r *run) update(p Document, err error) bool {
	if err != nil {
		r.fail(err)
		r.lastOutcome = "failed"
		r.page.Controls = nil
		return false
	}
	if !validURL(p.URL) {
		r.fail(errors.New("web_search: invalid final page URL"))
		r.lastOutcome = "failed"
		r.page.Controls = nil
		return false
	}
	key, digest := pageURL(p.URL), contentHash(p)
	previous, seen := r.hashes[key]
	changed := !seen || previous != digest
	if seen && changed {
		// A newer version of a source must not inherit completion based on
		// its old passages, including when the evidence count stays equal.
		kept := r.out.Evidence[:0]
		for _, evidence := range r.out.Evidence {
			if pageURL(evidence.URL) != key {
				kept = append(kept, evidence)
			}
		}
		r.out.Evidence = kept
		for passage := range r.collected {
			if strings.HasPrefix(passage, key+"\n") {
				delete(r.collected, passage)
			}
		}
		r.checkedEvidence = -1
	}
	if !changed {
		// Reuse the accepted body so whitespace-only responses cannot yield
		// duplicate passages, while fresh CDP control refs stay usable.
		p.Text = r.pageBodies[key]
	} else {
		r.pageBodies[key] = p.Text
	}
	if pageURL(r.page.URL) != key {
		r.pageAccepted = nil
	}
	// Keep current CDP refs/controls even when content is unchanged. They are
	// needed for site search, but are never treated as fresh source evidence.
	r.page = p
	r.hashes[key] = digest
	r.visited[key] = true
	r.out.LastPage = &PageObservation{URL: p.URL, Title: clip(p.Title, 200), Preview: clip(p.Text, 500), Links: len(p.Links), Controls: len(p.Controls), Truncated: p.Truncated, ContentHash: digest}
	if len(r.history) == 0 || pageURL(r.history[len(r.history)-1]) != key {
		r.history = append(r.history, p.URL)
	}
	if !changed {
		r.blockedURLs[key] = true
		r.lastOutcome = "unchanged"
		r.lastError = "Revisit rejected: source content hash is unchanged. Select another source, use site search, or refine the query."
	} else {
		delete(r.blockedURLs, key)
		r.lastOutcome = "changed"
		r.lastError = "Source content loaded."
	}
	return changed
}
