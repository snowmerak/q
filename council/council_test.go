package council_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/council"
	"github.com/snowmerak/q/tools"
)

func testCouncil(scope council.Scope, root, project string) council.Council {
	return council.Council{
		Name: "Architecture", Scope: scope, WorkspaceRoot: root, ProjectID: project,
		Members: []council.Seat{{Model: "provider/one"}, {Model: "provider/two"}}, Chair: council.Seat{Model: "provider/chair"},
	}
}

func TestStorePersistsIndependentCouncilAndTurns(t *testing.T) {
	configDir := t.TempDir()
	store := council.NewStore(configDir)
	t.Cleanup(func() { _ = store.Close() })
	created, err := store.Create(testCouncil(council.Independent, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "council", "independent", created.ID, "council.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("independent council path: %v", err)
	}
	if created.Rounds != 2 {
		t.Fatalf("default rounds = %d", created.Rounds)
	}
	turn, err := council.NewTurn(created, "What should we do?")
	if err != nil {
		t.Fatal(err)
	}
	turn.Status, turn.Final = "completed", "A considered answer"
	if err := store.SaveTurn(turn); err != nil {
		t.Fatal(err)
	}
	reopened := council.NewStore(configDir)
	t.Cleanup(func() { _ = reopened.Close() })
	loaded, err := reopened.Load(created.ID)
	if err != nil || loaded.WorkspaceRoot != "" || loaded.ProjectID != "" {
		t.Fatalf("reopened council = %+v, %v", loaded, err)
	}
	turns, err := reopened.ListTurns(created.ID)
	if err != nil || len(turns) != 1 || turns[0].Final != turn.Final {
		t.Fatalf("reopened turns = %+v, %v", turns, err)
	}
	updated := loaded
	updated.Chair.Model = "provider/new-chair"
	updated.Rounds = 4
	updated, err = reopened.Update(updated)
	if err != nil || updated.Chair.Model != "provider/new-chair" || updated.Rounds != 4 {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	turns, err = reopened.ListTurns(created.ID)
	if err != nil || turns[0].Chair.Model != "provider/chair" || turns[0].TotalRounds != 2 {
		t.Fatalf("historical model snapshot changed: %+v, %v", turns, err)
	}
}

func TestStoreLoadsLegacyCouncilWithTwoRounds(t *testing.T) {
	configDir := t.TempDir()
	store := council.NewStore(configDir)
	t.Cleanup(func() { _ = store.Close() })
	created, err := store.Create(testCouncil(council.Independent, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "council", "independent", created.ID, "council.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), "  \"rounds\": 2,\n", "", 1))
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(created.ID)
	if err != nil || loaded.Rounds != 2 {
		t.Fatalf("legacy council = %+v, %v", loaded, err)
	}
}

func TestStoreSeparatesWorkspaceAndProjectCouncils(t *testing.T) {
	store := council.NewStore(t.TempDir())
	t.Cleanup(func() { _ = store.Close() })
	root := t.TempDir()
	workspaceCouncil, err := store.Create(testCouncil(council.Workspace, root, ""))
	if err != nil {
		t.Fatal(err)
	}
	projectCouncil, err := store.Create(testCouncil(council.Project, "", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"))
	if err != nil {
		t.Fatal(err)
	}
	values, err := store.List()
	if err != nil || len(values) != 2 {
		t.Fatalf("list = %+v, %v", values, err)
	}
	if workspaceCouncil.Scope != council.Workspace || projectCouncil.Scope != council.Project {
		t.Fatalf("scopes = %s, %s", workspaceCouncil.Scope, projectCouncil.Scope)
	}
	if _, err := os.Stat(filepath.Join(store.Root, "projects", projectCouncil.ProjectID, projectCouncil.ID, "council.json")); err != nil {
		t.Fatal(err)
	}
}

func TestStoreSharesCouncilIndexBetweenInstances(t *testing.T) {
	configDir := t.TempDir()
	first := council.NewStore(configDir)
	second := council.NewStore(configDir)
	t.Cleanup(func() { _ = first.Close(); _ = second.Close() })
	if values, err := first.List(); err != nil || len(values) != 0 {
		t.Fatalf("initial list = %+v, %v", values, err)
	}
	if values, err := second.List(); err != nil || len(values) != 0 {
		t.Fatalf("second initial list = %+v, %v", values, err)
	}
	created, err := first.Create(testCouncil(council.Independent, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if values, err := second.List(); err != nil || len(values) != 1 || values[0].ID != created.ID {
		t.Fatalf("second instance did not see create: %+v, %v", values, err)
	}
	created.Name = "Revised architecture"
	if _, err := second.Update(created); err != nil {
		t.Fatal(err)
	}
	if values, err := first.List(); err != nil || len(values) != 1 || values[0].Name != created.Name {
		t.Fatalf("first instance did not see update: %+v, %v", values, err)
	}
	if err := first.Delete(created.ID); err != nil {
		t.Fatal(err)
	}
	if values, err := second.List(); err != nil || len(values) != 0 {
		t.Fatalf("second instance did not see delete: %+v, %v", values, err)
	}
}

func TestStoreImportsExistingCouncilJSON(t *testing.T) {
	configDir := t.TempDir()
	initial := council.NewStore(configDir)
	t.Cleanup(func() { _ = initial.Close() })
	created, err := initial.Create(testCouncil(council.Independent, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(configDir, "council", "index.sqlite")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "council", "independent", created.ID, "council.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(body), "  \"rounds\": 2,\n", "", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	migrated := council.NewStore(configDir)
	t.Cleanup(func() { _ = migrated.Close() })
	values, err := migrated.List()
	if err != nil || len(values) != 1 || values[0].ID != created.ID || values[0].Rounds != 2 {
		t.Fatalf("imported list = %+v, %v", values, err)
	}
}

func TestCouncilIndexProcessHelper(t *testing.T) {
	configDir := os.Getenv("Q_COUNCIL_INDEX_HELPER_DIR")
	if configDir == "" {
		return
	}
	store := council.NewStore(configDir)
	defer store.Close()
	if _, err := store.Create(testCouncil(council.Independent, "", "")); err != nil {
		t.Fatal(err)
	}
}

func TestStoreSharesIndexAcrossProcesses(t *testing.T) {
	configDir := t.TempDir()
	store := council.NewStore(configDir)
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.List(); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() {
			command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCouncilIndexProcessHelper$")
			command.Env = append(os.Environ(), "Q_COUNCIL_INDEX_HELPER_DIR="+configDir)
			if output, err := command.CombinedOutput(); err != nil {
				results <- fmt.Errorf("second process failed: %w\n%s", err, output)
				return
			}
			results <- nil
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	values, err := store.List()
	if err != nil || len(values) != 2 {
		t.Fatalf("first process did not see concurrent creates: %+v, %v", values, err)
	}
}

func TestExportMarkdownIncludesPartialRoundsAndLegacyTurns(t *testing.T) {
	value := testCouncil(council.Independent, "", "")
	value.ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	value.Rounds = 3
	partial, err := council.NewTurn(value, "Which option is safer?")
	if err != nil {
		t.Fatal(err)
	}
	partial.Status, partial.Stage = "running", "reviews"
	partial.Rounds = []council.Round{
		{Number: 1, Responses: []council.Response{{Label: "A", Model: "provider/one", Text: "First answer"}, {Label: "B", Model: "provider/two", Text: "Second answer"}}},
		{Number: 2, Reviews: []council.Review{{Model: "provider/one", Text: "Second answer is stronger", Ranking: []string{"B"}}}},
		{},
	}
	legacy := partial
	legacy.ID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	legacy.Status, legacy.Stage = "completed", "complete"
	legacy.Rounds = nil
	legacy.Responses = partial.Rounds[0].Responses
	legacy.Reviews = partial.Rounds[1].Reviews
	legacy.Final = "Chair conclusion"
	markdown := council.ExportMarkdown(value, []council.Turn{partial, legacy})
	for _, text := range []string{"Which option is safer?", "First answer", "Second answer", "Second answer is stronger", "Chair conclusion", "Turn 2", "Round 2"} {
		if !strings.Contains(markdown, text) {
			t.Fatalf("export omitted %q:\n%s", text, markdown)
		}
	}
	if strings.Contains(markdown, "Round 3") {
		t.Fatalf("future round leaked into partial export:\n%s", markdown)
	}
	if strings.Contains(markdown, "Ranking:") {
		t.Fatalf("plain-text export included historical ranking:\n%s", markdown)
	}
}

func TestLegacyJSONReviewExportsOnlyAnalysis(t *testing.T) {
	value := testCouncil(council.Independent, "", "")
	value.ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	turn, err := council.NewTurn(value, "Compare approaches")
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = "completed"
	turn.Rounds = []council.Round{
		{Number: 1, Responses: []council.Response{{Label: "A", Model: "provider/one", Text: "Original answer"}}},
		{Number: 2, Reviews: []council.Review{{Model: "provider/two", Text: "```json\n{\"analysis\":\"Useful criticism\",\"ranking\":[\"A\"],\"revised_answer\":\"Long duplicate answer\"}\n```"}}},
	}
	if got := council.ReviewAnalysis(turn.Rounds[1].Reviews[0].Text); got != "Useful criticism" {
		t.Fatalf("analysis = %q", got)
	}
	markdown := council.ExportMarkdown(value, []council.Turn{turn})
	if !strings.Contains(markdown, "Useful criticism") || strings.Contains(markdown, "Long duplicate answer") || strings.Contains(markdown, "Ranking:") {
		t.Fatalf("legacy review export was not reduced to analysis:\n%s", markdown)
	}
}

type fakeTools struct{ calls []string }

func (*fakeTools) Tools() []client.Tool {
	return []client.Tool{
		{Function: client.FunctionDefinition{Name: "read_file"}},
		{Function: client.FunctionDefinition{Name: "edit_file"}},
		{Function: client.FunctionDefinition{Name: "run_command"}},
	}
}
func (*fakeTools) Environment() tools.HostEnvironment { return tools.HostEnvironment{} }
func (f *fakeTools) Call(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
	f.calls = append(f.calls, call.Function.Name)
	return client.ToolResult{Content: "ok"}, nil
}

func TestReadRuntimeRejectsHiddenCallsAndSearchHonorsIgnore(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".qignore"), []byte("secret.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "visible.txt"), []byte("needle here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("needle hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := &fakeTools{}
	runtime := council.NewReadRuntime(base, []string{root})
	for _, tool := range runtime.Tools() {
		if tool.Function.Name == "edit_file" || tool.Function.Name == "run_command" {
			t.Fatalf("mutating tool advertised: %s", tool.Function.Name)
		}
	}
	for _, name := range []string{"edit_file", "run_command"} {
		if _, err := runtime.Call(t.Context(), client.ToolCall{Function: client.FunctionCall{Name: name}}); err == nil {
			t.Fatalf("%s was callable", name)
		}
	}
	if len(base.calls) != 0 {
		t.Fatalf("forbidden call reached runtime: %+v", base.calls)
	}
	result, err := runtime.Call(t.Context(), client.ToolCall{Function: client.FunctionCall{Name: "search_text", Arguments: `{"query":"needle"}`}})
	if err != nil || !strings.Contains(result.Content, "visible.txt") || strings.Contains(result.Content, "secret.txt") {
		t.Fatalf("search = %q, %v", result.Content, err)
	}
}

type fakeModel struct {
	mu       sync.Mutex
	requests []client.ChatRequest
}

type failingLastReviewModel struct {
	fakeModel
	mu       sync.Mutex
	failed   bool
	attempts int
}

func (m *failingLastReviewModel) Chat(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	if request.Model == "provider/two" && len(request.Messages) > 1 && strings.Contains(request.Messages[1].Content, "Round 3 reviews:") {
		m.mu.Lock()
		m.attempts++
		fail := !m.failed
		m.failed = true
		m.mu.Unlock()
		if fail {
			return nil, errors.New("temporary timeout")
		}
	}
	return m.fakeModel.Chat(ctx, request)
}

func TestResumeRetriesOnlyFailedReviewThenSynthesizes(t *testing.T) {
	value := testCouncil(council.Independent, "", "")
	value.ID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	value.Rounds = 4
	turn, err := council.NewTurn(value, "Assess the design")
	if err != nil {
		t.Fatal(err)
	}
	model := &failingLastReviewModel{}
	scratch := filepath.Join(t.TempDir(), "scratch")
	partial, err := council.Run(t.Context(), model, value, nil, turn, nil, scratch, 2, func(council.Turn) error { return nil })
	if err == nil || partial.Stage != "reviews" || partial.CurrentRound != 4 || !strings.Contains(err.Error(), "temporary timeout") {
		t.Fatalf("partial run = %+v, %v", partial, err)
	}
	if partial.Rounds[3].Reviews[0].Text == "" || partial.Rounds[3].Reviews[1].Error == "" {
		t.Fatalf("round 4 progress = %+v", partial.Rounds[3])
	}
	before := len(model.requests)
	kept := partial.Rounds[3].Reviews[0].Text
	encoded, err := json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	var saved council.Turn
	if err := json.Unmarshal(encoded, &saved); err != nil {
		t.Fatal(err)
	}
	result, err := council.Resume(t.Context(), model, value, nil, saved, nil, scratch, 2, func(council.Turn) error { return nil })
	if err != nil || result.Status != "completed" || result.Rounds[3].Reviews[0].Text != kept || result.Rounds[3].Reviews[1].Error != "" {
		t.Fatalf("resumed run = %+v, %v", result, err)
	}
	if len(model.requests)-before != 2 || model.attempts != 2 {
		t.Fatalf("resume made %d successful calls and %d failed-review attempts", len(model.requests)-before, model.attempts)
	}
}

func (m *fakeModel) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	m.mu.Lock()
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	content := "first answer from " + request.Model
	if strings.Contains(request.Messages[0].Content, "anonymous peer reviewer") {
		content = "Peer review from " + request.Model
	}
	if strings.Contains(request.Messages[0].Content, "continuing a multi-round LLM council") {
		content = "Integrated review from " + request.Model
	}
	if strings.Contains(request.Messages[0].Content, "chair of an LLM council") {
		content = "Council conclusion"
	}
	return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, Content: content}}}}, nil
}

func TestIndependentRunUsesDistinctOpinionsThenReviewsAndSynthesis(t *testing.T) {
	value := testCouncil(council.Independent, "", "")
	value.ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	turn, err := council.NewTurn(value, "Assess the design")
	if err != nil {
		t.Fatal(err)
	}
	model := &fakeModel{}
	var stages []string
	var opinionProgress, reviewProgress []int
	result, err := council.Run(t.Context(), model, value, nil, turn, nil, filepath.Join(t.TempDir(), "scratch"), 2, func(progress council.Turn) error {
		if len(stages) == 0 || stages[len(stages)-1] != progress.Stage {
			stages = append(stages, progress.Stage)
		}
		if progress.Stage == "opinions" {
			ready := 0
			for _, response := range progress.Responses {
				if response.Text != "" {
					ready++
				}
			}
			opinionProgress = append(opinionProgress, ready)
		}
		if progress.Stage == "reviews" {
			ready := 0
			for _, review := range progress.Reviews {
				if review.Text != "" {
					ready++
				}
			}
			reviewProgress = append(reviewProgress, ready)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.Final != "Council conclusion" || len(result.Responses) != 2 || len(result.Reviews) != 2 || len(result.Ranking) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if strings.Join(stages, ",") != "opinions,reviews,synthesis,complete" {
		t.Fatalf("stages = %+v", stages)
	}
	if !slices.Contains(opinionProgress, 1) || !slices.Contains(reviewProgress, 1) {
		t.Fatalf("member progress was not published: opinions=%v reviews=%v", opinionProgress, reviewProgress)
	}
	model.mu.Lock()
	defer model.mu.Unlock()
	for _, request := range model.requests {
		if len(request.Messages) > 0 && strings.Contains(request.Messages[0].Content, "anonymous peer reviewer") {
			if strings.Contains(request.Messages[1].Content, "provider/one") || strings.Contains(request.Messages[1].Content, "provider/two") {
				t.Fatalf("review leaked model identity: %s", request.Messages[1].Content)
			}
		}
	}
}

func TestCouncilContinuesReviewRoundsAndChairReceivesEveryRound(t *testing.T) {
	value := testCouncil(council.Independent, "", "")
	value.ID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	value.Rounds = 4
	turn, err := council.NewTurn(value, "Assess the design")
	if err != nil {
		t.Fatal(err)
	}
	model := &fakeModel{}
	result, err := council.Run(t.Context(), model, value, nil, turn, nil, filepath.Join(t.TempDir(), "scratch"), 2, func(council.Turn) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalRounds != 4 || result.CurrentRound != 4 || len(result.Rounds) != 4 || result.Status != "completed" {
		t.Fatalf("rounds = %+v", result)
	}
	if len(result.Rounds[1].Reviews) != 2 || len(result.Rounds[2].Reviews) != 2 || len(result.Rounds[3].Reviews) != 2 || len(result.Rounds[2].Responses) != 0 {
		t.Fatalf("round contents = %+v", result.Rounds)
	}
	if !strings.Contains(result.Rounds[2].Reviews[0].Text, "Integrated review") || len(result.Ranking) != 0 {
		t.Fatalf("round 3 was not plain-text review: %+v", result.Rounds[2])
	}
	model.mu.Lock()
	defer model.mu.Unlock()
	if len(model.requests) != 9 {
		t.Fatalf("model calls = %d, want 9", len(model.requests))
	}
	var chairRequest *client.ChatRequest
	var sawIntegratedFeedback, sawPriorReview bool
	for index := range model.requests {
		request := &model.requests[index]
		if request.Model == "provider/chair" {
			chairRequest = request
		}
		if strings.Contains(request.Messages[0].Content, "continuing a multi-round LLM council") {
			if strings.Contains(request.Messages[1].Content, "Round 2 reviews") {
				sawIntegratedFeedback = true
			}
			if strings.Contains(request.Messages[1].Content, "Peer review from") {
				sawPriorReview = true
			}
		}
	}
	if chairRequest == nil || !strings.Contains(chairRequest.Messages[1].Content, "Round 4") || !sawIntegratedFeedback || !sawPriorReview || !strings.Contains(chairRequest.Messages[1].Content, "Integrated review from") {
		t.Fatalf("later reviews were not integrated: chair=%v feedback=%v review=%v", chairRequest != nil, sawIntegratedFeedback, sawPriorReview)
	}
}

func TestCouncilCanFinishAfterOneRound(t *testing.T) {
	value := testCouncil(council.Independent, "", "")
	value.ID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	value.Rounds = 1
	turn, err := council.NewTurn(value, "Assess the design")
	if err != nil {
		t.Fatal(err)
	}
	model := &fakeModel{}
	result, err := council.Run(t.Context(), model, value, nil, turn, nil, filepath.Join(t.TempDir(), "scratch"), 2, func(council.Turn) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || len(result.Rounds) != 1 || len(result.Reviews) != 0 {
		t.Fatalf("single round result = %+v", result)
	}
	model.mu.Lock()
	defer model.mu.Unlock()
	if len(model.requests) != 3 {
		t.Fatalf("model calls = %d, want 3", len(model.requests))
	}
}

type emptyChairModel struct{ fakeModel }

func (m *emptyChairModel) Chat(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	if request.Model == "provider/chair" {
		return &client.ChatResponse{
			ID:      "response-123",
			Usage:   client.Usage{PromptTokens: 321, CompletionTokens: 64},
			Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant}, FinishReason: "length"}},
		}, nil
	}
	return m.fakeModel.Chat(ctx, request)
}

func TestChairEmptyAnswerReportsResponseDiagnostics(t *testing.T) {
	value := testCouncil(council.Independent, "", "")
	value.ID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	turn, err := council.NewTurn(value, "Assess the design")
	if err != nil {
		t.Fatal(err)
	}
	result, err := council.Run(t.Context(), &emptyChairModel{}, value, nil, turn, nil, filepath.Join(t.TempDir(), "scratch"), 2, func(council.Turn) error { return nil })
	if err == nil || result.Stage != "synthesis" {
		t.Fatalf("chair failure = %+v, %v", result, err)
	}
	for _, detail := range []string{"provider/chair", "empty answer", "finish_reason=length", "response_id=response-123", "prompt_tokens=321", "completion_tokens=64"} {
		if !strings.Contains(err.Error(), detail) {
			t.Fatalf("missing %q from error %q", detail, err)
		}
	}
}

func TestResumeFailedChairUsesSavedRounds(t *testing.T) {
	value := testCouncil(council.Independent, "", "")
	value.ID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	turn, err := council.NewTurn(value, "Assess the design")
	if err != nil {
		t.Fatal(err)
	}
	partial, err := council.Run(t.Context(), &emptyChairModel{}, value, nil, turn, nil, filepath.Join(t.TempDir(), "first"), 2, func(council.Turn) error { return nil })
	if err == nil || partial.Stage != "synthesis" {
		t.Fatalf("chair failure = %+v, %v", partial, err)
	}
	model := &fakeModel{}
	result, err := council.Resume(t.Context(), model, value, nil, partial, nil, filepath.Join(t.TempDir(), "resume"), 2, func(council.Turn) error { return nil })
	if err != nil || result.Status != "completed" || result.Final != "Council conclusion" {
		t.Fatalf("chair resume = %+v, %v", result, err)
	}
	if len(model.requests) != 1 || model.requests[0].Model != "provider/chair" {
		t.Fatalf("chair resume made unexpected calls: %+v", model.requests)
	}
}

type workspaceModel struct {
	mu                  sync.Mutex
	sawTool             bool
	forbiddenAdvertised bool
	counts              map[string]int
}

func (m *workspaceModel) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	m.mu.Lock()
	if m.counts == nil {
		m.counts = map[string]int{}
	}
	m.counts[request.Model]++
	count := m.counts[request.Model]
	m.mu.Unlock()
	for _, tool := range request.Tools {
		if tool.Function.Name == "edit_file" || tool.Function.Name == "run_command" {
			m.mu.Lock()
			m.forbiddenAdvertised = true
			m.mu.Unlock()
		}
	}
	answer := "peer review"
	if request.Model == "provider/chair" {
		answer = "repository synthesis"
	}
	if len(request.Tools) > 0 {
		for _, message := range request.Messages {
			if message.Role == client.RoleTool {
				m.mu.Lock()
				m.sawTool = true
				m.mu.Unlock()
				return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, Content: "located the evidence"}}}}, nil
			}
		}
		if count > 2 {
			return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, Content: "located the evidence"}}}}, nil
		}
		return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
			ID: "search-1", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "search_text", Arguments: `{"query":"needle"}`},
		}}}}}}, nil
	}
	return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, Content: answer}}}}, nil
}

func TestWorkspaceRunUsesQToolLoopWithReadOnlyCatalog(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "evidence.txt"), []byte("needle in file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value := testCouncil(council.Workspace, root, "")
	value.ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	turn, err := council.NewTurn(value, "Find evidence")
	if err != nil {
		t.Fatal(err)
	}
	model := &workspaceModel{}
	result, err := council.Run(t.Context(), model, value, nil, turn, []string{root}, filepath.Join(t.TempDir(), "scratch"), 2, func(council.Turn) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Final != "repository synthesis" {
		t.Fatalf("final = %q", result.Final)
	}
	model.mu.Lock()
	defer model.mu.Unlock()
	if !model.sawTool || model.forbiddenAdvertised {
		t.Fatalf("tool round = %v, forbidden advertised = %v", model.sawTool, model.forbiddenAdvertised)
	}
}
