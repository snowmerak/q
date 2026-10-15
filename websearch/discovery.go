package websearch

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/snowmerak/q/diagnostics"

	"github.com/JoakimCarlsson/scour/engines"
	"github.com/JoakimCarlsson/scour/merge"
	"github.com/JoakimCarlsson/scour/query"
	"github.com/JoakimCarlsson/scour/rank"
)

// Candidate is a discovery hint, not verified source evidence.
type Candidate struct {
	URL     string   `json:"url"`
	Title   string   `json:"title"`
	Snippet string   `json:"snippet,omitempty"`
	Engines []string `json:"engines,omitempty"`
	Score   float64  `json:"score"`
}

type SearchResults struct {
	Candidates []Candidate
	Errors     []string
}

// MetaSearcher supports concurrent, independent calls and caller cancellation.
type MetaSearcher interface {
	Search(context.Context, string) (SearchResults, error)
}

// ScourSearcher fans out only to general web engines. It does not run scour's
// answer/infobox plugins: Q collects and verifies original page text itself.
type ScourSearcher struct{}

func (ScourSearcher) Search(ctx context.Context, text string) (SearchResults, error) {
	q := query.Query{Raw: text, Terms: text, Category: query.CategoryGeneral, SafeSearch: query.SafeModerate, Page: 1}
	selected := engines.Select(q, engines.Preferences{})
	results, failures := engines.FanOut(ctx, q, selected, 8*time.Second)
	counts := map[string]int{}
	for _, result := range results {
		counts[result.Engine]++
	}
	for engine, count := range counts {
		diagnostics.Logger(ctx).Info("web_search.metasearch.engine.completed", "engine", clip(engine, 80), "result_count", count, "reason_code", "none")
	}
	for _, failure := range failures {
		diagnostics.Logger(ctx).Info("web_search.metasearch.engine.completed", "engine", clip(failure.Engine, 80), "result_count", 0, "reason_code", failureCode(failure.Err), "suspended", failure.Suspended)
	}
	if err := ctx.Err(); err != nil {
		return SearchResults{}, err
	}
	return mergeSearchResults(results, failures), nil
}

func mergeSearchResults(results []engines.Result, failures []engines.FanOutError) SearchResults {
	out := SearchResults{}
	seen := map[string]bool{}
	for _, result := range rank.Rank(merge.Merge(results), nil) {
		key := pageURL(result.URL)
		if !validURL(result.URL) || len(result.URL) > 2000 || seen[key] {
			continue
		}
		seen[key] = true
		candidate := Candidate{URL: result.URL, Title: clip(result.Title, 200), Snippet: clip(result.Snippet, 500), Score: result.Score}
		for _, source := range result.Sources {
			candidate.Engines = append(candidate.Engines, source.Engine)
		}
		out.Candidates = append(out.Candidates, candidate)
		if len(out.Candidates) == 20 {
			break
		}
	}
	for _, failure := range failures {
		out.Errors = append(out.Errors, clip(fmt.Sprintf("metasearch: %s", &failure), 600))
	}
	return out
}

func (r *run) discover(ctx context.Context, text string) {
	if r.engine.Discovery == nil {
		r.load(ctx, searchURL(text), false)
		return
	}
	r.out.SearchCalls++
	started := time.Now()
	diagnostics.Logger(ctx).Info("web_search.metasearch.started", "query_id", logID(text), "query_bytes", len(text))
	results, err := r.engine.Discovery.Search(ctx, text)
	r.out.Candidates = nil
	r.page = Document{}
	r.pageAccepted = nil
	if err != nil {
		diagnostics.Logger(ctx).Info("web_search.metasearch.completed", "reason_code", failureCode(err), "duration_ms", time.Since(started).Milliseconds(), "candidate_count", 0)
		r.fail(err)
		return
	}
	seen := map[string]bool{}
	for _, candidate := range results.Candidates {
		key := pageURL(candidate.URL)
		reason := ""
		switch {
		case !validURL(candidate.URL) || len(candidate.URL) > 2000:
			reason = "invalid_url"
		case seen[key]:
			reason = "duplicate"
		}
		if reason != "" {
			diagnostics.Logger(ctx).Info("web_search.candidate.filtered", "url", logURL(candidate.URL), "url_id", logID(candidate.URL), "reason_code", reason)
			continue
		}
		seen[key] = true
		candidate.Title = clip(candidate.Title, 200)
		candidate.Snippet = clip(candidate.Snippet, 500)
		r.out.Candidates = append(r.out.Candidates, candidate)
		diagnostics.Logger(ctx).Info("web_search.candidate.discovered", "candidate_id", fmt.Sprintf("candidate_%d", len(r.out.Candidates)-1), "rank", len(r.out.Candidates), "url", logURL(candidate.URL), "url_id", logID(candidate.URL), "engines", candidate.Engines)
		if len(r.out.Candidates) == 20 {
			break
		}
	}
	for _, message := range results.Errors {
		r.fail(fmt.Errorf("%s", message))
	}
	diagnostics.Logger(ctx).Info("web_search.metasearch.completed", "candidate_count", len(r.out.Candidates), "returned_count", len(results.Candidates), "engine_error_count", len(results.Errors), "duration_ms", time.Since(started).Milliseconds(), "reason_code", "none")
	if len(r.out.Candidates) == 0 {
		r.lastError = "Metasearch returned no usable candidates. Refine the query or stop with unresolved requirements."
	} else {
		r.lastError = fmt.Sprintf("Metasearch returned %d candidate pages. Snippets are unverified; open a source page to collect evidence.", len(r.out.Candidates))
	}
}

func (r *run) candidateChoices() []int {
	var indices []int
	for _, revisit := range []bool{false, true} {
		for i, candidate := range r.out.Candidates {
			key := pageURL(candidate.URL)
			if len(indices) >= 8 || r.visited[key] != revisit || key == pageURL(r.page.URL) || r.blockedURLs[key] || r.rejected[candidate.URL] {
				continue
			}
			indices = append(indices, i)
		}
	}
	return indices
}

// pageURL removes fragments and volatile search-page parameters, retaining
// source-page query parameters because they can identify different documents.
func pageURL(target string) string {
	u, err := url.Parse(target)
	if err != nil {
		return target
	}
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	q := u.Query()
	if searchResults(target) {
		for key := range q {
			if key != "q" && key != "start" && key != "first" && key != "s" {
				q.Del(key)
			}
		}
	} else {
		for key := range q {
			lower := strings.ToLower(key)
			if strings.HasPrefix(lower, "utm_") || lower == "gclid" || lower == "fbclid" {
				q.Del(key)
			}
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}
