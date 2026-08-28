package library

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

type fakePropositionJudgeClient struct {
	requests         []client.ChatRequest
	terminalRequests []client.ChatRequest
	terminalErr      error
	message          client.Message
	errors           map[string]error
}

func (f *fakePropositionJudgeClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	if request.ToolChoice == client.ToolChoiceNone && len(request.Messages) > 0 && request.Messages[len(request.Messages)-1].Role == client.RoleTool {
		f.terminalRequests = append(f.terminalRequests, request)
		return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant}, FinishReason: "stop"}}}, f.terminalErr
	}
	f.requests = append(f.requests, request)
	if err := f.errors[request.Model]; err != nil {
		return nil, err
	}
	return &client.ChatResponse{Choices: []client.Choice{{Message: f.message}}}, nil
}

func (*fakePropositionJudgeClient) Close() error { return nil }

func TestConfiguredPropositionJudgeUsesLibrarianRole(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()

	directory := t.TempDir()
	value := config.Default()
	value.Provider.Model = "main-model"
	value.Provider.BaseURL = server.URL
	value.ModelGroups = map[string]config.ModelGroupConfig{
		"library": {Candidates: []config.ModelCandidateConfig{
			{Model: "librarian-model", ReasoningEffort: "medium", Timeout: 20 * time.Second},
			{Model: "backup-model", ReasoningEffort: "high"},
		}},
	}
	value.Agents.Roles = map[string]config.AgentConfig{
		config.AgentRoleThinker:   {Model: "thinker-model", ReasoningEffort: "high"},
		config.AgentRoleLibrarian: {Group: "library"},
	}
	if err := (config.Store{Dir: directory}).Save(value); err != nil {
		t.Fatal(err)
	}

	judge, err := newConfiguredPropositionJudge(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer judge.Close()
	if judge.model != "librarian-model" || judge.effort != "medium" || judge.group != "library" ||
		len(judge.candidates) != 2 || judge.candidates[0].Timeout != 20*time.Second {
		t.Fatalf("configured judge = %#v", judge)
	}
}

func TestModelPropositionJudgeUsesFreshStrictDecisionSession(t *testing.T) {
	configured := &fakePropositionJudgeClient{message: client.Message{
		Role: client.RoleAssistant,
		ToolCalls: []client.ToolCall{{
			ID: "decision", Type: client.ToolTypeFunction,
			Function: client.FunctionCall{
				Name:      "resolve_proposition",
				Arguments: `{"action":"merge","target_id":"prop-existing","reason":"same truth condition"}`,
			},
		}},
	}}
	judge := &modelPropositionJudge{client: configured, model: "librarian-model", effort: "high"}
	decision, err := judge.JudgeProposition(context.Background(), PropositionRegisterRequest{
		Content: "The Library uses a durable queue.", Confidence: 0.9,
	}, []PropositionSearchHit{{
		ID: "prop-existing", Content: "Library registration is durably queued.", Score: 0.91,
	}})
	if err != nil || decision.Action != PropositionActionMerge || decision.TargetID != "prop-existing" {
		t.Fatalf("decision = %#v, err = %v", decision, err)
	}
	if len(configured.requests) != 1 {
		t.Fatalf("requests = %d", len(configured.requests))
	}
	if len(configured.terminalRequests) != 1 {
		t.Fatalf("decision result was not returned: %#v", configured.terminalRequests)
	}
	terminal := configured.terminalRequests[0]
	last := terminal.Messages[len(terminal.Messages)-1]
	if last.ToolCallID != "decision" || last.Name != "resolve_proposition" || !strings.Contains(last.Content, `"target_id":"prop-existing"`) {
		t.Fatalf("decision result = %#v", last)
	}
	request := configured.requests[0]
	if request.Model != "librarian-model" || request.ReasoningEffort != "high" ||
		request.ToolChoice != client.ToolChoiceRequired || request.ParallelToolCalls == nil || *request.ParallelToolCalls ||
		len(request.Messages) != 2 || !strings.Contains(request.Messages[1].Content, `"score":0.91`) {
		t.Fatalf("judge request = %#v", request)
	}
}

func TestModelPropositionJudgeFallsBackOnTransientModelFailure(t *testing.T) {
	configured := &fakePropositionJudgeClient{
		message: client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
			ID: "decision", Type: client.ToolTypeFunction,
			Function: client.FunctionCall{Name: "resolve_proposition", Arguments: `{"action":"create","target_id":"","reason":"distinct"}`},
		}}},
		errors: map[string]error{
			"primary": &client.APIError{StatusCode: http.StatusServiceUnavailable, Message: "temporary"},
		},
	}
	judge := &modelPropositionJudge{
		client: configured, model: "primary", group: "library", router: client.NewModelRouter(),
		candidates: []client.ModelCandidate{{Model: "primary"}, {Model: "secondary"}},
	}
	decision, err := judge.JudgeProposition(t.Context(), PropositionRegisterRequest{Content: "A distinct fact."}, nil)
	if err != nil || decision.Action != PropositionActionCreate {
		t.Fatalf("decision = %#v, err = %v", decision, err)
	}
	if len(configured.requests) != 2 || configured.requests[0].Model != "primary" || configured.requests[1].Model != "secondary" {
		t.Fatalf("requests = %#v", configured.requests)
	}
	if len(configured.terminalRequests) != 1 || configured.terminalRequests[0].Model != "secondary" {
		t.Fatalf("terminal result must stay on the selected provider: %#v", configured.terminalRequests)
	}
}

func TestModelPropositionJudgePropagatesTerminalDeliveryFailure(t *testing.T) {
	failure := errors.New("terminal delivery failed")
	configured := &fakePropositionJudgeClient{terminalErr: failure, message: client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
		ID: "decision", Function: client.FunctionCall{Name: "resolve_proposition", Arguments: `{"action":"create","target_id":"","reason":"distinct"}`},
	}}}}
	_, err := (&modelPropositionJudge{client: configured, model: "librarian"}).JudgeProposition(t.Context(), PropositionRegisterRequest{Content: "A fact."}, nil)
	if !errors.Is(err, failure) || len(configured.terminalRequests) != 1 {
		t.Fatalf("err=%v terminalRequests=%d", err, len(configured.terminalRequests))
	}
}

func TestModelPropositionJudgeDoesNotFallbackOnStructuredResponseFailure(t *testing.T) {
	configured := &fakePropositionJudgeClient{message: client.Message{Role: client.RoleAssistant, Content: "plain text"}}
	judge := &modelPropositionJudge{
		client: configured, model: "primary", group: "library", router: client.NewModelRouter(),
		candidates: []client.ModelCandidate{{Model: "primary"}, {Model: "secondary"}},
	}
	_, err := judge.JudgeProposition(t.Context(), PropositionRegisterRequest{Content: "A fact."}, nil)
	if err == nil || !strings.Contains(err.Error(), "exactly once") {
		t.Fatalf("error = %v", err)
	}
	if len(configured.requests) != 1 || configured.requests[0].Model != "primary" {
		t.Fatalf("requests = %#v", configured.requests)
	}
}
