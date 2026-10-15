package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/snowmerak/q/client/systemone"
)

func pageAnswers(req systemone.Request, choose func(string, systemone.Question) string) *systemone.Result {
	result := &systemone.Result{Answers: map[string]systemone.Answer{}}
	for key, question := range req.Questions {
		result.Answers[key] = systemone.Answer{Type: systemone.QuestionChoice, Choice: choose(key, question)}
	}
	return result
}

func TestPageResearchClassifiesWholePageBeforeCoverage(t *testing.T) {
	paragraph := strings.Repeat("context ", 150)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		for i := range 6 {
			_, _ = fmt.Fprintf(w, "<p>Fact %d: %s</p>", i, paragraph)
		}
	}))
	defer page.Close()
	classified, checks := 0, 0
	d := decisionFunc(func(_ context.Context, req systemone.Request, _ systemone.CallOptions) (*systemone.Result, error) {
		return pageAnswers(req, func(key string, q systemone.Question) string {
			switch {
			case key == "publisher":
				return "accept"
			case strings.HasPrefix(key, "block_"):
				if !strings.Contains(q.Instructions.(string), fmt.Sprintf("state.blocks[%q]", key)) {
					t.Fatal("classification target not in effective question", q)
				}
				classified++
				return "keep"
			default:
				checks++
				if classified != 6 {
					t.Fatalf("completion ran after %d of six blocks", classified)
				}
				return "supported"
			}
		}), nil
	})
	e := Engine{Decision: d, Discovery: metaSearchFunc(func(context.Context, string) (SearchResults, error) {
		t.Fatal("start URL unexpectedly searched")
		return SearchResults{}, nil
	})}
	out, err := e.Search(t.Context(), Input{Goal: "Find all six facts", StartURL: page.URL, MaxSteps: 1})
	if err != nil || out.Status != "complete" || classified != 6 || checks != 2 || out.CoverageCalls != 1 || out.PlannerCalls != 0 || out.Steps != 1 || len(out.Pages) != 1 || out.Pages[0].Examined != 6 || len(out.Evidence) != 6 {
		t.Fatalf("out=%+v err=%v classified=%d checks=%d", out, err, classified, checks)
	}
}

func TestPageResearchPartialEvidenceContinuesAcrossFailuresAndDuplicates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/unavailable":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/first", "/duplicate":
			_, _ = fmt.Fprint(w, "<main><p>Alpha is blue.</p></main>")
		case "/last":
			_, _ = fmt.Fprint(w, "<main><p>Beta is green.</p></main>")
		default:
			t.Error("unexpected URL", r.URL)
		}
	}))
	defer server.Close()
	selected, coverage := 0, 0
	e := Engine{Discovery: metaSearchFunc(func(context.Context, string) (SearchResults, error) {
		return SearchResults{Candidates: []Candidate{{URL: server.URL + "/unavailable"}, {URL: server.URL + "/first"}, {URL: server.URL + "/duplicate"}, {URL: server.URL + "/last"}}}, nil
	}), Decision: decisionFunc(func(_ context.Context, req systemone.Request, _ systemone.CallOptions) (*systemone.Result, error) {
		if _, ok := req.Questions["requirement_0"]; ok {
			coverage++
		}
		return pageAnswers(req, func(key string, _ systemone.Question) string {
			switch {
			case key == "page":
				id := fmt.Sprintf("candidate_%d", selected)
				selected++
				return id
			case key == "publisher":
				return "accept"
			case strings.HasPrefix(key, "block_"):
				return "keep"
			default:
				if len(req.State.(map[string]any)["evidence"].([]Evidence)) == 2 {
					return "supported"
				}
				return "missing"
			}
		}), nil
	})}
	out, err := e.Search(t.Context(), Input{Goal: "Alpha and Beta colors", MaxSteps: 4})
	if err != nil || out.Status != "complete" || out.SearchCalls != 1 || len(out.Evidence) != 2 || out.Steps != 4 || coverage != 2 || len(out.Pages) != 4 || out.Pages[0].Error == "" || out.Pages[2].Outcome != "unchanged" {
		t.Fatalf("out=%+v err=%v coverage=%d", out, err, coverage)
	}
}

func TestPageResearchPreservesOriginalGoalAndRejectsWrongPublisher(t *testing.T) {
	u := "https://official.test/current"
	third := "https://third.test/history"
	s := &fakeSession{pages: map[string]Document{u: doc(u, "Education minister Example."), third: doc(third, "An earlier minister was Someone Else.")}}
	checks := 0
	goal := "Use only official sources for current education and agriculture ministers as of today."
	e := Engine{NewSession: func() Session { return s }, Discovery: metaSearchFunc(func(context.Context, string) (SearchResults, error) {
		return SearchResults{Candidates: []Candidate{{URL: third}}}, nil
	}), Decision: decisionFunc(func(_ context.Context, req systemone.Request, _ systemone.CallOptions) (*systemone.Result, error) {
		if _, ok := req.Questions["requirement_0"]; ok {
			checks++
			state := req.State.(map[string]any)
			if state["original_goal"] != goal || !strings.Contains(req.Questions["requirement_0"].Instructions.(string), "source, time") || !strings.Contains(req.Questions["requirement_1"].Instructions.(string), goal) {
				t.Fatal("source/time qualification or original-goal guard lost", req)
			}
		}
		return pageAnswers(req, func(key string, _ systemone.Question) string {
			switch {
			case key == "page":
				return "candidate_0"
			case key == "publisher":
				if req.State.(map[string]string)["url"] == third {
					return "reject"
				}
				return "accept"
			case strings.HasPrefix(key, "block_"):
				return "keep"
			case key == "requirement_0":
				return "supported"
			default:
				return "missing"
			}
		}), nil
	})}
	out, err := e.Search(t.Context(), Input{Goal: goal, Requirements: []string{"Education minister name"}, StartURL: u, MaxSteps: 3})
	if err != nil || out.Status != "incomplete" || checks != 1 || len(out.Evidence) != 1 || out.Unresolved[0] != goal || !s.closed || out.Pages[1].Outcome != "rejected" {
		t.Fatalf("out=%+v err=%v checks=%d", out, err, checks)
	}
}

func TestPageResearchWireBudgetIsExplicitAndKeepsEvidence(t *testing.T) {
	u := "https://example.test/page"
	// One entire paragraph must remain intact instead of being silently clipped.
	text := "Known fact." + "\n\n" + strings.Repeat("x", maxDecisionBytes)
	s := &fakeSession{pages: map[string]Document{u: doc(u, text)}}
	d := decisionFunc(func(_ context.Context, req systemone.Request, _ systemone.CallOptions) (*systemone.Result, error) {
		body, _ := json.Marshal(req)
		if len(body) > maxDecisionBytes {
			t.Fatal("oversized native call", len(body))
		}
		return pageAnswers(req, func(key string, _ systemone.Question) string {
			if key == "publisher" {
				return "accept"
			}
			if strings.HasPrefix(key, "block_") {
				return "keep"
			}
			return "missing"
		}), nil
	})
	e := Engine{Decision: d, NewSession: func() Session { return s }, Discovery: metaSearchFunc(func(context.Context, string) (SearchResults, error) {
		t.Fatal("budget failure should not search again")
		return SearchResults{}, nil
	})}
	out, err := e.Search(t.Context(), Input{Goal: "Known fact and more", StartURL: u})
	if err != nil || out.Status != "incomplete" || out.StopReason != "evidence_budget" || len(out.Evidence) != 1 || !out.Pages[0].Truncated || out.Pages[0].Examined != 1 || !s.closed {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestPageResearchNativeFailureIncludesProviderDetail(t *testing.T) {
	u := "https://example.test/page"
	s := &fakeSession{pages: map[string]Document{u: doc(u, "Useful fact.")}}
	e := Engine{NewSession: func() Session { return s }, Discovery: metaSearchFunc(func(context.Context, string) (SearchResults, error) { return SearchResults{}, nil }), Decision: decisionFunc(func(_ context.Context, req systemone.Request, _ systemone.CallOptions) (*systemone.Result, error) {
		if _, ok := req.Questions["publisher"]; ok {
			return pageAnswers(req, func(string, systemone.Question) string { return "accept" }), nil
		}
		return nil, &systemone.APIError{StatusCode: 422, Body: []byte(`{"detail":"prompt has 4719 tokens; limit is 4096; truncation is disabled"}`)}
	})}
	out, err := e.Search(t.Context(), Input{Goal: "Useful fact", StartURL: u})
	if err != nil || out.Status != "incomplete" || !strings.Contains(out.LastObservation, "limit is 4096") || len(out.SystemOneTimings) != 2 || out.SystemOneTimings[1].Phase != "relevance" || !s.closed {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestPageResearchRejectsRedirectOutsideAllowedPublishers(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "<main>Unapproved source fact.</main>")
	}))
	defer page.Close()
	destination := strings.Replace(page.URL, "127.0.0.1", "localhost", 1)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination, http.StatusFound) }))
	defer redirect.Close()
	e := Engine{Discovery: metaSearchFunc(func(context.Context, string) (SearchResults, error) {
		t.Fatal("page budget should prevent search")
		return SearchResults{}, nil
	}), Decision: decisionFunc(func(context.Context, systemone.Request, systemone.CallOptions) (*systemone.Result, error) {
		t.Fatal("disallowed publisher reached a model")
		return nil, nil
	})}
	out, err := e.Search(t.Context(), Input{Goal: "Approved evidence", StartURL: redirect.URL, AllowedDomains: []string{"127.0.0.1"}, MaxSteps: 1})
	if err != nil || out.Status != "incomplete" || len(out.Evidence) != 0 || out.SystemOneCalls != 0 || out.Pages[0].Outcome != "rejected" || !strings.Contains(out.LastObservation, "Redirected publisher") {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestPageResearchCancellationRetainsCollectedEvidence(t *testing.T) {
	u := "https://example.test/page"
	s := &fakeSession{pages: map[string]Document{u: doc(u, "Useful fact.")}}
	e := Engine{NewSession: func() Session { return s }, Discovery: metaSearchFunc(func(context.Context, string) (SearchResults, error) { return SearchResults{}, nil }), Decision: sequence(t, "accept"), CoverageDecision: decisionFunc(func(context.Context, systemone.Request, systemone.CallOptions) (*systemone.Result, error) {
		return nil, context.Canceled
	})}
	out, err := e.Search(t.Context(), Input{Goal: "Useful fact", StartURL: u})
	if err != nil || out.Status != "incomplete" || out.StopReason != "coverage_check_error" || len(out.Evidence) != 1 || !strings.Contains(out.LastObservation, "canceled") || !s.closed {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}
