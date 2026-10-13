package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/systemoneconfig"
	"github.com/snowmerak/q/websearch"
)

type fakeWebSearcher struct{ calls int }

type batchSearchFunc func(context.Context, websearch.Input) (websearch.Output, error)

func (f batchSearchFunc) Search(ctx context.Context, in websearch.Input) (websearch.Output, error) {
	return f(ctx, in)
}

func (s *fakeWebSearcher) Search(_ context.Context, in websearch.Input) (websearch.Output, error) {
	s.calls++
	return websearch.Output{Status: "complete", Requirements: []string{in.Goal}, Evidence: []websearch.Evidence{{URL: "https://example.com", Text: "Captured web_search evidence"}}}, nil
}

func TestWebSearchMCPAndLoomIntegration(t *testing.T) {
	s := &fakeWebSearcher{}
	r, err := newRuntimeWithLSP(t.Context(), sameRuntimeRoots(t.TempDir()), nil, nil, loom.NewProcessEvaluator(), loom.StoreOptions{}, nil, nil, WithWebSearch(s))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if !runtimeHasTool(r, "web_search") {
		t.Fatal("web_search missing")
	}
	result, err := r.Call(t.Context(), client.ToolCall{ID: "web-call", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "web_search", Arguments: `{"goal":"research goal"}`}})
	if err != nil || result.IsError || s.calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, s.calls)
	}
	var receipt loomReceipt
	if err := json.Unmarshal([]byte(result.Content), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.LoomRef == "" || !receipt.Stored || receipt.Result == nil {
		t.Fatalf("receipt=%+v", receipt)
	}
	artifact, err := r.loom.Store.Inspect(t.Context(), receipt.LoomRef)
	if err != nil || artifact.Source["tool"] != "web_search" {
		t.Fatalf("artifact=%+v err=%v", artifact, err)
	}
}

func TestWebSearchBatchMCPAndLoomIntegration(t *testing.T) {
	var calls atomic.Int32
	search := batchSearchFunc(func(_ context.Context, in websearch.Input) (websearch.Output, error) {
		calls.Add(1)
		return websearch.Output{Status: "complete", Evidence: []websearch.Evidence{{URL: "https://example.com/" + in.Goal, Text: in.Goal}}, SystemOneCalls: 4, CoverageCalls: 1}, nil
	})
	runtime, err := newRuntimeWithLSP(t.Context(), sameRuntimeRoots(t.TempDir()), nil, nil, loom.NewProcessEvaluator(), loom.StoreOptions{}, nil, nil, WithWebSearch(search))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	if !runtimeHasTool(runtime, "web_search_batch") {
		t.Fatal("batch tool missing")
	}
	result, err := runtime.Call(t.Context(), client.ToolCall{ID: "batch-call", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "web_search_batch", Arguments: `{"tasks":[{"id":"first","search":{"goal":"one"}},{"id":"second","search":{"goal":"two"}}],"concurrency":2}`}})
	if err != nil || result.IsError || calls.Load() != 2 {
		t.Fatalf("result=%+v error=%v calls=%d", result, err, calls.Load())
	}
	var receipt loomReceipt
	if err := json.Unmarshal([]byte(result.Content), &receipt); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(receipt.Result)
	if err != nil {
		t.Fatal(err)
	}
	var out websearch.BatchOutput
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "complete" || len(out.Results) != 2 || out.SystemOneCalls != 8 || out.CoverageCalls != 2 || out.Results[0].Result.Evidence[0].Text != "one" || out.Results[1].Result.Evidence[0].Text != "two" {
		t.Fatalf("batch=%+v", out)
	}
	artifact, err := runtime.loom.Store.Inspect(t.Context(), receipt.LoomRef)
	if err != nil || artifact.Source["tool"] != "web_search_batch" {
		t.Fatalf("artifact=%+v error=%v", artifact, err)
	}
}

func TestLargeWebSearchBatchPreviewRetainsAllTaskOutcomes(t *testing.T) {
	search := batchSearchFunc(func(_ context.Context, in websearch.Input) (websearch.Output, error) {
		evidence := make([]websearch.Evidence, 8)
		for i := range evidence {
			evidence[i] = websearch.Evidence{URL: "https://example.com/" + in.Goal, Text: strings.Repeat("x", 1400)}
		}
		return websearch.Output{Status: "complete", Evidence: evidence}, nil
	})
	runtime, err := newRuntimeWithLSP(t.Context(), sameRuntimeRoots(t.TempDir()), nil, nil, loom.NewProcessEvaluator(), loom.StoreOptions{}, nil, nil, WithWebSearch(search))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	var tasks []websearch.BatchTask
	for i := range 8 {
		tasks = append(tasks, websearch.BatchTask{ID: fmt.Sprintf("task%d-%s", i, strings.Repeat("z", 58)), Search: websearch.Input{Goal: fmt.Sprintf("goal%d", i)}})
	}
	arguments, err := json.Marshal(websearch.BatchInput{Tasks: tasks})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Call(t.Context(), client.ToolCall{ID: "large-batch", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "web_search_batch", Arguments: string(arguments)}})
	if err != nil || result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var receipt loomReceipt
	if err := json.Unmarshal([]byte(result.Content), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Result != nil || receipt.Preview == "" {
		t.Fatal("large result did not use a bounded preview")
	}
	end := strings.Index(receipt.Preview, `"results":`)
	if end < 0 {
		end = len(receipt.Preview)
	}
	for _, task := range tasks {
		if !strings.Contains(receipt.Preview[:end], `"id":"`+task.ID+`"`) {
			t.Fatalf("summary lost %s: %s", task.ID, receipt.Preview)
		}
	}
	if !strings.Contains(receipt.Preview[:end], `"status":"complete"`) {
		t.Fatal("summary lost batch completion")
	}
	body, err := runtime.loom.Store.ReadAll(t.Context(), receipt.LoomRef, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Structured websearch.BatchOutput `json:"structured"`
	}
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Structured.Results) != 8 || len(saved.Structured.Results[7].Result.Evidence) != 8 || len(saved.Structured.Results[7].Result.Evidence[7].Text) != 1400 {
		t.Fatal("full evidence was lost")
	}
}

func TestWebPlannerOnlyWritesRequestedFieldsAndUsesConfiguredProvider(t *testing.T) {
	var calls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("leaked ambient API key")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "qwen3.8-27b" || body["tools"] != nil {
			t.Errorf("unexpected request model/tools: %+v", body)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		answer := `{"query":"new search terms"}`
		if calls == 2 {
			answer = `{"query":"not allowed","requirements":["x"]}`
		}
		response := map[string]any{"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "JSON follows:\n" + answer}, "finish_reason": "stop"}}}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	}))
	defer api.Close()
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	dir := t.TempDir()
	if err := (providerhost.Store{Dir: dir}).Save(gateway.Config{Providers: []gateway.ProviderConfig{{ID: "oev", Prefix: "oev", Type: "openai-compatible", Enabled: true, BaseURL: api.URL + "/v1"}}}); err != nil {
		t.Fatal(err)
	}
	p := &webQueryPlanner{dir: dir, model: defaultWebPlannerModel}
	plan, err := p.Plan(t.Context(), websearch.PlanRequest{Operation: "query", Goal: "Research"})
	if err != nil || plan.Query != "new search terms" || calls != 1 {
		t.Fatalf("plan=%+v err=%v calls=%d", plan, err, calls)
	}
	if _, err := p.Plan(t.Context(), websearch.PlanRequest{Operation: "decompose", Goal: "Research"}); err == nil {
		t.Fatal("accepted query in decomposition")
	}
	if _, err := p.Plan(t.Context(), websearch.PlanRequest{Operation: "navigate"}); err == nil || calls != 2 {
		t.Fatal("unsupported operation reached model")
	}
}

func TestWebSearchSettingsStrictAndLazyPlanner(t *testing.T) {
	dir := t.TempDir()
	s, err := loadWebSearchSettings(dir)
	if err != nil || s.PlannerModel != defaultWebPlannerModel {
		t.Fatalf("settings=%+v err=%v", s, err)
	}
	for _, body := range []string{`null`, `{"unknown":true}`, `{"planner_model":"unqualified"}`, `{"planner_model":"oev/qwen3.8-27b"} {}`, strings.Repeat("x", 4097)} {
		if err := os.WriteFile(filepath.Join(dir, "websearch.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadWebSearchSettings(dir); err == nil {
			t.Fatalf("accepted %s", fmt.Sprintf("%.100s", body))
		}
	}
}

func TestConfiguredWebSearchUsesNativeRoleWithoutLoadingPlannerProvider(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<main>Evidence for the exact requirement.</main>")
	}))
	defer page.Close()
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("unexpected generative request %s", r.URL.Path)
		}
		var request systemone.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "gemma4-e4b" {
			t.Errorf("model=%s", request.Model)
		}
		answers := map[string]systemone.Answer{}
		for k := range request.Questions {
			choice := "supported"
			switch k {
			case "action":
				choice = "collect_0"
				if calls > 0 {
					choice = "finish"
				}
			case "publisher":
				choice = "accept"
			}
			answers[k] = systemone.Answer{Type: systemone.QuestionChoice, Choice: choice}
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(systemone.Result{Answers: answers}); err != nil {
			t.Error(err)
		}
	}))
	defer api.Close()
	dir := t.TempDir()
	so := systemoneconfig.Default()
	so.Providers = []systemoneconfig.ProviderConfig{{ID: "oev", URI: api.URL + "/v1/systemone"}}
	so.DefaultModel = "oev/another-default"
	so.RoleModels = map[string]string{systemoneconfig.RoleWebSearch: "oev/gemma4-e4b"}
	if err := (systemoneconfig.Store{Dir: dir}).Save(so); err != nil {
		t.Fatal(err)
	}
	out, err := (&configuredWebSearch{dir: dir}).Search(t.Context(), websearch.Input{Goal: "the exact requirement", StartURL: page.URL})
	if err != nil || out.Status != "complete" || out.PlannerCalls != 0 || out.SystemOneCalls != 4 || out.CoverageModel != "gemma4-e4b" || out.CoverageCalls != 1 {
		t.Fatalf("out=%+v err=%v calls=%d", out, err, calls)
	}
	// No providers.json exists: planner setup is deferred until its operation.
	if _, err := os.Stat((providerhost.Store{Dir: dir}).Path()); !os.IsNotExist(err) {
		t.Fatal("unexpected planner provider file")
	}
}

func TestConfiguredWebSearchRoutesCompletionToSeparateNativeModel(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<main>Evidence for the exact requirement.</main>")
	}))
	defer page.Close()
	navigationCalls, coverageCalls := 0, 0
	newAPI := func(model string, coverage bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req systemone.Request
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if r.URL.Path != "/v1/systemone" || req.Model != model {
				t.Errorf("path=%s model=%s", r.URL.Path, req.Model)
			}
			answers := map[string]systemone.Answer{}
			for k := range req.Questions {
				choice := "supported"
				if coverage {
					if !strings.HasPrefix(k, "requirement_") {
						t.Errorf("non-completion question sent to Qwen: %s", k)
					}
				} else {
					switch k {
					case "action":
						choice = "collect_0"
						if navigationCalls > 0 {
							choice = "finish"
						}
					case "publisher":
						choice = "accept"
					default:
						t.Errorf("completion leaked to Gemma: %s", k)
					}
				}
				answers[k] = systemone.Answer{Type: systemone.QuestionChoice, Choice: choice}
			}
			if coverage {
				coverageCalls++
			} else {
				navigationCalls++
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(systemone.Result{Answers: answers}); err != nil {
				t.Error(err)
			}
		}))
	}
	navigation := newAPI("gemma4-e4b", false)
	defer navigation.Close()
	coverage := newAPI("qwen3.8-27b", true)
	defer coverage.Close()
	dir := t.TempDir()
	so := systemoneconfig.Default()
	so.Providers = []systemoneconfig.ProviderConfig{{ID: "oev", URI: navigation.URL + "/v1/systemone"}, {ID: "judge", URI: coverage.URL + "/v1/systemone"}}
	so.DefaultModel = "oev/gemma4-e4b"
	so.RoleModels = map[string]string{systemoneconfig.RoleWebSearchFinish: "judge/qwen3.8-27b"}
	if err := (systemoneconfig.Store{Dir: dir}).Save(so); err != nil {
		t.Fatal(err)
	}
	out, err := (&configuredWebSearch{dir: dir}).Search(t.Context(), websearch.Input{Goal: "the exact requirement", StartURL: page.URL})
	if err != nil || out.Status != "complete" || out.PlannerCalls != 0 || out.CoverageCalls != 1 || out.CoverageModel != "qwen3.8-27b" || navigationCalls != 3 || coverageCalls != 1 || out.SystemOneCalls != 4 {
		t.Fatalf("output=%+v err=%v navigation=%d coverage=%d", out, err, navigationCalls, coverageCalls)
	}
}
