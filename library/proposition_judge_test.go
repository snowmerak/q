package library

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

type fakePropositionJudgeClient struct {
	requests []client.ChatRequest
	message  client.Message
}

func (f *fakePropositionJudgeClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	f.requests = append(f.requests, request)
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
	value.Agents.Roles = map[string]config.AgentConfig{
		config.AgentRoleThinker:   {Model: "thinker-model", ReasoningEffort: "high"},
		config.AgentRoleLibrarian: {Model: "librarian-model", ReasoningEffort: "medium"},
	}
	if err := (config.Store{Dir: directory}).Save(value); err != nil {
		t.Fatal(err)
	}

	judge, err := newConfiguredPropositionJudge(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer judge.Close()
	if judge.model != "librarian-model" || judge.effort != "medium" {
		t.Fatalf("configured judge = model %q, effort %q", judge.model, judge.effort)
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
	request := configured.requests[0]
	if request.Model != "librarian-model" || request.ReasoningEffort != "high" ||
		request.ToolChoice != client.ToolChoiceRequired || request.ParallelToolCalls == nil || *request.ParallelToolCalls ||
		len(request.Messages) != 2 || !strings.Contains(request.Messages[1].Content, `"score":0.91`) {
		t.Fatalf("judge request = %#v", request)
	}
}
