package websearch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/JoakimCarlsson/scour/engines"
	"github.com/snowmerak/q/client/systemone"
)

type metaSearchFunc func(context.Context, string) (SearchResults, error)

func (f metaSearchFunc) Search(ctx context.Context, query string) (SearchResults, error) {
	return f(ctx, query)
}

func TestMetasearchSelectsAndReadsOriginalSource(t *testing.T) {
	pageCalls := 0
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageCalls++
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<main>The lantern is blue.</main>")
	}))
	defer page.Close()
	searchCalls := 0
	base := sequence(t, "candidate_1", "accept")
	engine := Engine{Discovery: metaSearchFunc(func(_ context.Context, query string) (SearchResults, error) {
		searchCalls++
		if query != "lantern specification" || pageCalls != 0 {
			t.Fatal("source fetched before candidate selection")
		}
		return SearchResults{Candidates: []Candidate{{URL: page.URL + "/other", Title: "Other", Snippet: "The lantern is red."}, {URL: page.URL, Title: "Specification", Snippet: "Unverified search snippet."}}, Errors: []string{"bing: blocked"}}, nil
	}), Decision: decisionFunc(func(ctx context.Context, req systemone.Request, opts systemone.CallOptions) (*systemone.Result, error) {
		if q, ok := req.Questions["page"]; ok && searchCalls == 1 && pageCalls == 0 {
			choices := q.Criteria.(map[string]string)
			if choices["candidate_1"] == "" || choices["collect_0"] != "" || choices["finish"] != "" {
				t.Fatal("search snippets were offered as verified evidence")
			}
		}
		return base.Evaluate(ctx, req, opts)
	}), NewSession: func() Session { return NewBrowserSession() }}
	out, err := engine.Search(t.Context(), Input{Goal: "What color is the lantern?", Query: "lantern specification"})
	if err != nil || out.Status != "complete" || out.SearchCalls != 1 || searchCalls != 1 || pageCalls != 1 || len(out.Evidence) != 1 || out.Evidence[0].Text != "The lantern is blue." || len(out.Errors) != 1 || out.LastPage.ContentHash == "" {
		t.Fatalf("out=%+v err=%v search=%d pages=%d", out, err, searchCalls, pageCalls)
	}
}

func TestMetasearchExhaustionReturnsWithoutPlannerEscalation(t *testing.T) {
	u := "https://official.test/detail"
	session := &fakeSession{pages: map[string]Document{u: doc(u, "The exact requirement is documented.")}}
	var queries []string
	engine := Engine{Discovery: metaSearchFunc(func(_ context.Context, query string) (SearchResults, error) {
		queries = append(queries, query)
		if len(queries) == 1 {
			return SearchResults{}, nil
		}
		return SearchResults{Candidates: []Candidate{{URL: u}}}, nil
	}), Decision: sequence(t), Planner: plannerFunc(func(_ context.Context, req PlanRequest) (Plan, error) {
		t.Fatal("page workflow must not invoke a generative planner", req)
		return Plan{}, nil
	}), NewSession: func() Session { return session }}
	out, err := engine.Search(t.Context(), Input{Goal: "exact requirement", Query: "first query"})
	if err != nil || out.Status != "incomplete" || out.StopReason != "candidate_exhausted" || out.SearchCalls != 1 || out.PlannerCalls != 0 || len(queries) != 1 || !session.closed {
		t.Fatalf("out=%+v err=%v queries=%v", out, err, queries)
	}
}

func TestScourMergesConsensusAndRetainsEngineFailures(t *testing.T) {
	out := mergeSearchResults([]engines.Result{
		{Engine: "google", Position: 1, URL: "https://example.com/docs", Title: "Docs"},
		{Engine: "bing", Position: 1, URL: "https://example.com/docs", Title: "Docs"},
		{Engine: "brave", Position: 2, URL: "https://example.com/other", Title: "Other"},
		{Engine: "bing", Position: 3, URL: "javascript:bad", Title: "Bad"},
	}, []engines.FanOutError{{Engine: "duckduckgo", Err: errors.New("blocked")}})
	if len(out.Candidates) != 2 || out.Candidates[0].URL != "https://example.com/docs" || len(out.Candidates[0].Engines) != 2 || len(out.Errors) != 1 {
		t.Fatalf("out=%+v", out)
	}
}

func TestMetasearchCancellationStopsBeforeModelCall(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	session := &fakeSession{}
	engine := Engine{Discovery: metaSearchFunc(func(ctx context.Context, _ string) (SearchResults, error) {
		cancel()
		return SearchResults{}, ctx.Err()
	}), Decision: sequence(t), NewSession: func() Session { return session }}
	out, err := engine.Search(ctx, Input{Goal: "research"})
	if err != nil || out.StopReason != "cancelled_or_timeout" || out.SystemOneCalls != 0 || !session.closed {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestLiveScourWebSearch(t *testing.T) {
	if os.Getenv("Q_TEST_SCOUR") != "1" {
		t.Skip("set Q_TEST_SCOUR=1 to query public search engines")
	}
	for _, query := range []string{"Go programming language official documentation", "대한민국 정부조직관리정보시스템"} {
		t.Run(query, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			out, err := (ScourSearcher{}).Search(ctx, query)
			engineNames := map[string]bool{}
			domains := map[string]bool{}
			for _, candidate := range out.Candidates {
				for _, engine := range candidate.Engines {
					engineNames[engine] = true
				}
				if u, parseErr := url.Parse(candidate.URL); parseErr == nil {
					domains[u.Hostname()] = true
				}
			}
			var names []string
			for engine := range engineNames {
				names = append(names, engine)
			}
			sort.Strings(names)
			t.Logf("candidates=%d engines=%v errors=%v", len(out.Candidates), names, out.Errors)
			if err != nil || len(out.Candidates) == 0 {
				t.Fatalf("live metasearch failed: %v", err)
			}
			if query == "대한민국 정부조직관리정보시스템" && !domains["org.go.kr"] && !domains["www.org.go.kr"] {
				t.Fatal("official organization site absent from Korean search candidates")
			}
		})
	}
}
