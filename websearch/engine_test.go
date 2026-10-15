package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/webbrowser"
)

type decisionFunc func(context.Context, systemone.Request, systemone.CallOptions) (*systemone.Result, error)

func (f decisionFunc) Evaluate(ctx context.Context, r systemone.Request, o systemone.CallOptions) (*systemone.Result, error) {
	return f(ctx, r, o)
}

type plannerFunc func(context.Context, PlanRequest) (Plan, error)

func (f plannerFunc) Plan(ctx context.Context, r PlanRequest) (Plan, error) { return f(ctx, r) }

type fakeSession struct {
	pages                        map[string]Document
	rendered                     Document
	filled, submitted            Document
	fillRef, fillText, submitRef string
	closed                       bool
}

func (s *fakeSession) Page(ctx context.Context, u string, render bool) (Document, error) {
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	if render {
		return s.rendered, nil
	}
	if p, ok := s.pages[u]; ok {
		return p, nil
	}
	return Document{}, fmt.Errorf("unknown fixture URL %s", u)
}
func (s *fakeSession) Fill(_ context.Context, ref, text string) (Document, error) {
	s.fillRef, s.fillText = ref, text
	return s.filled, nil
}

func (s *fakeSession) Read(context.Context) (Document, error) { return s.submitted, nil }
func (s *fakeSession) Submit(_ context.Context, ref string, _ bool) (Document, error) {
	s.submitRef = ref
	return s.submitted, nil
}
func (s *fakeSession) Close() error { s.closed = true; return nil }
func doc(u, text string) Document {
	return Document{PageContent: webbrowser.PageContent{URL: u, Title: "Fixture", Text: text}}
}

func sequence(t *testing.T, choices ...string) Decider {
	t.Helper()
	i := 0
	return decisionFunc(func(_ context.Context, r systemone.Request, _ systemone.CallOptions) (*systemone.Result, error) {
		b, _ := json.Marshal(r)
		if len(b) > 65536 {
			t.Fatalf("oversized request: %d", len(b))
		}
		out := &systemone.Result{Answers: map[string]systemone.Answer{}}
		for key, q := range r.Questions {
			options := q.Criteria.(map[string]string)
			if key == "action" {
				if state, ok := r.State.(map[string]any); ok {
					if ev, ok := state["evidence"].([]Evidence); ok && len(ev) == 0 && options["finish"] != "" {
						t.Fatal("completion offered without evidence")
					}
				}
			}
			if len(options) > 36 {
				t.Fatalf("too many options: %d", len(options))
			}
			choice := "supported"
			if strings.HasPrefix(key, "block_") {
				choice = "keep"
			}
			if key == "action" || key == "publisher" || key == "page" {
				if i >= len(choices) {
					t.Fatalf("unexpected decision %s", key)
				}
				choice = choices[i]
				i++
			}
			if options[choice] == "" {
				t.Fatalf("unavailable %s choice %q: %#v", key, choice, options)
			}
			out.Answers[key] = systemone.Answer{Type: systemone.QuestionChoice, Choice: choice}
		}
		return out, nil
	})
}

func TestSystemOneControlsInputAndSubmissionWithoutPlanner(t *testing.T) {
	u := "https://example.com/search"
	s := &fakeSession{pages: map[string]Document{u: doc(u, "")}}
	s.rendered = doc(u, "")
	s.rendered.Controls = []webbrowser.Element{{Ref: "old", InputType: "search", Role: "textbox", Name: "Search", Search: true}, {Ref: "password", InputType: "password", Role: "textbox"}}
	s.filled = doc(u, "")
	s.filled.Controls = []webbrowser.Element{{Ref: "fresh", InputType: "search", Role: "textbox", Name: "Search", Search: true, Value: "deadline exception"}}
	s.submitted = doc("https://example.com/result", "The deadline can be changed. Timeout raises an exception outside the block.")
	e := Engine{Decision: sequence(t, "render", "fill_0_0", "submit_0", "collect_0", "accept", "finish"), DecisionModel: "gemma4-e4b", Planner: plannerFunc(func(context.Context, PlanRequest) (Plan, error) {
		t.Fatal("unexpected generative call")
		return Plan{}, nil
	}), NewSession: func() Session { return s }}
	out, err := e.Search(t.Context(), Input{Goal: "Find deadline and exception behavior", Query: "deadline exception", Requirements: []string{"Deadline changes", "Exception behavior"}, StartURL: u})
	if err != nil || out.Status != "complete" || out.PlannerCalls != 0 || len(out.Evidence) != 1 || len(out.Unresolved) != 0 {
		t.Fatalf("output=%+v error=%v", out, err)
	}
	if s.fillRef != "old" || s.fillText != "deadline exception" || s.submitRef != "fresh" || !s.closed {
		t.Fatalf("session=%+v", s)
	}
}

func TestCompletionUsesIndependentSystemOneModel(t *testing.T) {
	for _, tc := range []struct {
		name     string
		maxSteps int
		fail     bool
	}{
		{name: "first source before next action", maxSteps: 20},
		{name: "budget boundary", maxSteps: 1},
		{name: "coverage error retains evidence", maxSteps: 2, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := "https://example.com/docs"
			s := &fakeSession{pages: map[string]Document{u: doc(u, "Explicit evidence.")}}
			base := sequence(t, "collect_0", "accept", "finish")
			e := Engine{DecisionModel: "gemma4-e4b", CoverageModel: "qwen3.8-27b", NewSession: func() Session { return s }}
			e.Decision = decisionFunc(func(ctx context.Context, req systemone.Request, opts systemone.CallOptions) (*systemone.Result, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > time.Minute || time.Until(deadline) < 55*time.Second {
					t.Fatal("navigation must retain its one-minute deadline")
				}
				if req.Model != "gemma4-e4b" {
					t.Fatalf("navigation model=%s", req.Model)
				}
				if _, ok := req.Questions["requirement_0"]; ok {
					t.Fatal("completion leaked to navigation model")
				}
				return base.Evaluate(ctx, req, opts)
			})
			e.CoverageDecision = decisionFunc(func(ctx context.Context, req systemone.Request, _ systemone.CallOptions) (*systemone.Result, error) {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 30*time.Minute || time.Until(deadline) < 29*time.Minute {
					t.Fatal("completion must allow thirty minutes without a shorter search deadline")
				}
				if req.Model != "qwen3.8-27b" || len(req.Questions) != 2 {
					t.Fatalf("completion request=%+v", req)
				}
				if tc.fail {
					return nil, errors.New("completion model unavailable")
				}
				answers := map[string]systemone.Answer{}
				for k, q := range req.Questions {
					choices := q.Criteria.(map[string]string)
					if choices["supported"] == "" || choices["missing"] == "" {
						t.Fatal("completion must use native choices")
					}
					answers[k] = systemone.Answer{Type: systemone.QuestionChoice, Choice: "supported"}
				}
				return &systemone.Result{Answers: answers}, nil
			})
			out, err := e.Search(t.Context(), Input{Goal: "Explicit evidence", StartURL: u, MaxSteps: tc.maxSteps})
			if err != nil || len(out.Evidence) != 1 || !s.closed || out.CoverageCalls != 1 || out.CoverageModel != "qwen3.8-27b" || out.SystemOneModel != "gemma4-e4b" || out.SystemOneCalls != 3 || out.Steps != 1 {
				t.Fatalf("output=%+v err=%v", out, err)
			}
			if tc.fail {
				if out.Status != "incomplete" || out.StopReason != "coverage_check_error" || len(out.Errors) != 1 {
					t.Fatalf("failed completion=%+v", out)
				}
			} else if out.Status != "complete" {
				t.Fatalf("completion=%+v", out)
			}
		})
	}
}

func TestNavigationUsesExplicitDecisionTimeout(t *testing.T) {
	u := "https://example.com/docs"
	base := sequence(t, "stop")
	e := Engine{NewSession: func() Session { return &fakeSession{pages: map[string]Document{u: doc(u, "Evidence.")}} }}
	e.Decision = decisionFunc(func(ctx context.Context, req systemone.Request, opts systemone.CallOptions) (*systemone.Result, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second || time.Until(deadline) < time.Second {
			t.Fatal("explicit navigation timeout was not applied")
		}
		return base.Evaluate(ctx, req, opts)
	})
	if _, err := e.Search(t.Context(), Input{Goal: "Evidence", StartURL: u, DecisionTimeoutSeconds: 2}); err != nil {
		t.Fatal(err)
	}
}

func TestQueryEscalationOnlyAfterMissingCoverage(t *testing.T) {
	u := "https://example.com/overview"
	s := &fakeSession{pages: map[string]Document{u: doc(u, "Overview, no exception details."), searchURL("timeout exception catch"): doc("https://example.com/detail", "The exception is caught outside the context.")}}
	base := sequence(t, "collect_0", "accept", "query", "collect_0", "accept", "finish")
	checks := 0
	planCalls := 0
	e := Engine{Decision: decisionFunc(func(ctx context.Context, r systemone.Request, o systemone.CallOptions) (*systemone.Result, error) {
		if _, ok := r.Questions["requirement_0"]; ok {
			checks++
			out := &systemone.Result{Answers: map[string]systemone.Answer{}}
			for k := range r.Questions {
				c := "supported"
				if checks == 1 {
					c = "missing"
				}
				out.Answers[k] = systemone.Answer{Type: systemone.QuestionChoice, Choice: c}
			}
			return out, nil
		}
		return base.Evaluate(ctx, r, o)
	}), Planner: plannerFunc(func(_ context.Context, r PlanRequest) (Plan, error) {
		planCalls++
		if r.Operation != "query" || len(r.Missing) == 0 || len(r.PreviousQueries) != 1 {
			t.Fatalf("plan=%+v", r)
		}
		return Plan{Query: "timeout exception catch"}, nil
	}), NewSession: func() Session { return s }}
	out, err := e.Search(t.Context(), Input{Goal: "Where can the exception be caught?", StartURL: u})
	if err != nil || out.Status != "complete" || planCalls != 1 || out.PlannerCalls != 1 || checks != 2 || !s.closed {
		t.Fatalf("output=%+v err=%v plans=%d checks=%d", out, err, planCalls, checks)
	}
}

func TestDecompositionCannotDropOriginalGoalAtCompletion(t *testing.T) {
	u := "https://example.com/docs"
	s := &fakeSession{pages: map[string]Document{u: doc(u, "Part one only.")}}
	base := sequence(t, "decompose", "collect_0", "accept", "stop")
	e := Engine{Decision: decisionFunc(func(ctx context.Context, r systemone.Request, o systemone.CallOptions) (*systemone.Result, error) {
		if _, ok := r.Questions["requirement_0"]; ok {
			return &systemone.Result{Answers: map[string]systemone.Answer{"requirement_0": {Type: systemone.QuestionChoice, Choice: "supported"}, "requirement_1": {Type: systemone.QuestionChoice, Choice: "missing"}}}, nil
		}
		return base.Evaluate(ctx, r, o)
	}), Planner: plannerFunc(func(_ context.Context, r PlanRequest) (Plan, error) {
		if r.Operation != "decompose" {
			t.Fatal(r)
		}
		return Plan{Requirements: []string{"part one"}}, nil
	}), NewSession: func() Session { return s }}
	out, err := e.Search(t.Context(), Input{Goal: "part one AND part two", StartURL: u})
	if err != nil || out.Status != "incomplete" || len(out.Unresolved) != 1 || out.Unresolved[0] != "part one AND part two" || out.PlannerCalls != 1 {
		t.Fatalf("output=%+v err=%v", out, err)
	}
}

func TestDomainConstraintRejectsSuffixImpostor(t *testing.T) {
	u := "https://official.test.attacker.test/docs"
	s := &fakeSession{pages: map[string]Document{u: doc(u, "Pretend official evidence.")}}
	e := Engine{Decision: sequence(t, "collect_0", "stop"), NewSession: func() Session { return s }}
	out, err := e.Search(t.Context(), Input{Goal: "Official evidence", AllowedDomains: []string{"official.test"}, StartURL: u})
	if err != nil || len(out.Evidence) != 0 || out.SystemOneCalls != 2 || out.Status != "incomplete" {
		t.Fatalf("output=%+v err=%v", out, err)
	}
}

func TestBudgetCancellationAndBadProtocolRetainPartialResults(t *testing.T) {
	t.Run("budget", func(t *testing.T) {
		u := "https://example.com"
		s := &fakeSession{pages: map[string]Document{u: doc(u, "Evidence.")}}
		e := Engine{Decision: sequence(t, "collect_0", "accept"), NewSession: func() Session { return s }}
		out, err := e.Search(t.Context(), Input{Goal: "Evidence", StartURL: u, MaxSteps: 1})
		if err != nil || out.Steps != 1 || !s.closed || out.Status != "complete" {
			t.Fatalf("output=%+v err=%v", out, err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		s := &fakeSession{}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		e := Engine{Decision: sequence(t), NewSession: func() Session { return s }}
		out, err := e.Search(ctx, Input{Goal: "Evidence"})
		if err != nil || !s.closed || out.Status != "incomplete" || out.SystemOneCalls != 0 {
			t.Fatalf("output=%+v err=%v", out, err)
		}
	})
	t.Run("invalid_choice", func(t *testing.T) {
		s := &fakeSession{pages: map[string]Document{searchURL("Evidence"): doc("https://example.com", "")}}
		e := Engine{Decision: decisionFunc(func(context.Context, systemone.Request, systemone.CallOptions) (*systemone.Result, error) {
			return &systemone.Result{Answers: map[string]systemone.Answer{"action": {Type: systemone.QuestionChoice, Choice: "invented_action"}}}, nil
		}), NewSession: func() Session { return s }}
		out, err := e.Search(t.Context(), Input{Goal: "Evidence"})
		if err != nil || !s.closed || out.StopReason != "decision_error" || len(out.Errors) == 0 {
			t.Fatalf("output=%+v err=%v", out, err)
		}
	})
	t.Run("planner_error", func(t *testing.T) {
		s := &fakeSession{}
		e := Engine{Decision: sequence(t, "query"), Planner: plannerFunc(func(context.Context, PlanRequest) (Plan, error) { return Plan{}, errors.New("planner down") }), NewSession: func() Session { return s }}
		out, err := e.Search(t.Context(), Input{Goal: "Evidence"})
		if err != nil || !s.closed || out.StopReason != "planner_error" {
			t.Fatalf("output=%+v err=%v", out, err)
		}
	})
}

func TestInvalidInputNeverCreatesSession(t *testing.T) {
	e := Engine{Decision: sequence(t), NewSession: func() Session { t.Fatal("unexpected session"); return nil }}
	for _, in := range []Input{{}, {Goal: "x", StartURL: "file:///private"}, {Goal: "x", AllowedDomains: []string{""}}, {Goal: "x", Requirements: []string{""}}, {Goal: "x", MaxSteps: 41}, {Goal: strings.Repeat("x", 4001)}} {
		if _, err := e.Search(t.Context(), in); err == nil {
			t.Fatalf("accepted %+v", in)
		}
	}
}

func TestFailedFinishCannotRepeatAtBudgetBoundary(t *testing.T) {
	u := "https://example.com/partial"
	s := &fakeSession{pages: map[string]Document{u: doc(u, "Only a partial answer.")}}
	base := sequence(t, "collect_0", "accept", "stop")
	checks := 0
	e := Engine{Decision: decisionFunc(func(ctx context.Context, r systemone.Request, o systemone.CallOptions) (*systemone.Result, error) {
		if _, ok := r.Questions["requirement_0"]; ok {
			checks++
			answer := &systemone.Result{Answers: map[string]systemone.Answer{}}
			for k := range r.Questions {
				answer.Answers[k] = systemone.Answer{Type: systemone.QuestionChoice, Choice: "missing"}
			}
			return answer, nil
		}
		return base.Evaluate(ctx, r, o)
	}), NewSession: func() Session { return s }}
	out, err := e.Search(t.Context(), Input{Goal: "Complete requirement", StartURL: u, MaxSteps: 2})
	if err != nil || out.Status != "incomplete" || checks != 1 || out.SystemOneCalls != 4 {
		t.Fatalf("out=%+v err=%v checks=%d", out, err, checks)
	}
}

func TestSearchChallengeStopsWithoutModelCalls(t *testing.T) {
	u := searchURL("research")
	s := &fakeSession{pages: map[string]Document{u: doc(u, "Unfortunately, bots use DuckDuckGo too. Please complete the challenge.")}}
	e := Engine{Decision: sequence(t), NewSession: func() Session { return s }}
	out, err := e.Search(t.Context(), Input{Goal: "research"})
	if err != nil || out.Status != "incomplete" || out.StopReason != "search_engine_challenge" || out.SystemOneCalls != 0 || !s.closed {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestBackConsumesHistoryInsteadOfTogglingPages(t *testing.T) {
	u := "https://example.com/search"
	p := doc(u, "")
	p.Links = []webbrowser.Link{{Text: "Detail", URL: "https://example.com/detail"}}
	s := &fakeSession{pages: map[string]Document{u: p, "https://example.com/detail": doc("https://example.com/detail", "")}}
	base := sequence(t, "open_0", "back", "stop")
	actions := 0
	e := Engine{Decision: decisionFunc(func(ctx context.Context, r systemone.Request, o systemone.CallOptions) (*systemone.Result, error) {
		if q, ok := r.Questions["action"]; ok {
			actions++
			if actions == 3 && q.Criteria.(map[string]string)["back"] != "" {
				t.Fatal("back action reintroduced the page just left")
			}
		}
		return base.Evaluate(ctx, r, o)
	}), NewSession: func() Session { return s }}
	out, err := e.Search(t.Context(), Input{Goal: "Find detail", StartURL: u})
	if err != nil || out.Steps != 3 || !s.closed {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}
