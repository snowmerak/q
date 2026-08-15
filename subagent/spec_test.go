package subagent

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

type groupChatClient struct {
	models []string
}

func (c *groupChatClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	c.models = append(c.models, request.Model)
	if request.Model == "primary" {
		return nil, &client.APIError{StatusCode: http.StatusServiceUnavailable, Message: "temporary"}
	}
	return &client.ChatResponse{}, nil
}

func TestResolveUsesRoleModelAndReasoningEffort(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "chat-model"
	value.Agents.Roles = map[string]config.AgentConfig{
		config.AgentRolePlanner: {Model: "planning-model", ReasoningEffort: "high"},
	}
	models := []client.Model{
		{ID: "chat-model"},
		{ID: "planning-model", ContextLength: 200_000, MaxOutputTokens: 16_000, Capabilities: &client.ModelCapabilities{Reasoning: &client.ReasoningCapabilities{
			Supported: true, Control: client.ReasoningControlEffort,
			SupportedEfforts: []string{"low", "medium", "high"}, DefaultEffort: "medium",
		}}},
	}

	spec, err := Resolve(value, config.AgentRolePlanner, models)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Role != config.AgentRolePlanner || spec.Model != "planning-model" || spec.ReasoningEffort != "high" {
		t.Fatalf("spec = %#v", spec)
	}
	if spec.Reasoning == nil || spec.Reasoning.DefaultEffort != "medium" || len(spec.Reasoning.SupportedEfforts) != 3 {
		t.Fatalf("reasoning = %#v", spec.Reasoning)
	}
	if spec.ContextLength != 200_000 || spec.MaxOutputTokens != 16_000 {
		t.Fatalf("model limits = %#v", spec)
	}
	request := client.ChatRequest{}
	spec.Apply(&request)
	if request.Model != "planning-model" || request.ReasoningEffort != "high" {
		t.Fatalf("request = %#v", request)
	}
}

func TestResolveInheritsActiveModelAndProviderDefaultEffort(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "chat-model"
	models := []client.Model{{
		ID: "chat-model", Capabilities: &client.ModelCapabilities{Reasoning: &client.ReasoningCapabilities{
			Supported: true, Control: client.ReasoningControlEffort,
			SupportedEfforts: []string{"low", "high"}, DefaultEffort: "high",
		}},
	}}
	spec, err := Resolve(value, config.AgentRoleScout, models)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Model != "chat-model" || spec.ReasoningEffort != "" || spec.Reasoning.DefaultEffort != "high" {
		t.Fatalf("spec = %#v", spec)
	}
	request := client.ChatRequest{ReasoningEffort: "high"}
	spec.Apply(&request)
	if request.ReasoningEffort != "" {
		t.Fatalf("empty role effort did not clear request effort: %#v", request)
	}
}

func TestResolveModelGroupAndKeepSuccessfulFallbackForExecution(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "chat-model"
	value.ModelGroups = map[string]config.ModelGroupConfig{
		"heavy": {Candidates: []config.ModelCandidateConfig{
			{Model: "primary", Timeout: time.Second},
			{Model: "secondary", ReasoningEffort: "medium"},
		}},
	}
	value.Agents.Roles = map[string]config.AgentConfig{
		config.AgentRolePlanner: {Group: "heavy", ReasoningEffort: "high"},
	}
	models := []client.Model{
		{ID: "primary", ContextLength: 200_000, MaxOutputTokens: 16_000, Capabilities: effortCapabilities("high", "medium")},
		{ID: "secondary", ContextLength: 128_000, MaxOutputTokens: 8_000, Capabilities: effortCapabilities("high", "medium")},
	}
	spec, err := Resolve(value, config.AgentRolePlanner, models)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Group != "heavy" || spec.Model != "primary" || spec.ReasoningEffort != "high" ||
		len(spec.Candidates) != 2 || spec.ContextLength != 128_000 || spec.MaxOutputTokens != 8_000 {
		t.Fatalf("spec = %#v", spec)
	}
	configured := &groupChatClient{}
	spec.Router = client.NewModelRouter()
	if _, err := spec.Chat(t.Context(), configured, client.ChatRequest{}); err != nil {
		t.Fatal(err)
	}
	if spec.Model != "secondary" || spec.ReasoningEffort != "medium" {
		t.Fatalf("active spec = %#v", spec)
	}
	if _, err := spec.Chat(t.Context(), configured, client.ChatRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(configured.models, ","); got != "primary,secondary,secondary" {
		t.Fatalf("model calls = %s", got)
	}
}

func TestResolveRejectsInvalidModelReasoningConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		effort string
		models []client.Model
		want   string
	}{
		{name: "missing model", model: "missing", models: []client.Model{{ID: "other"}}, want: "not found"},
		{name: "no reasoning", model: "plain", effort: "high", models: []client.Model{{ID: "plain"}}, want: "does not advertise"},
		{name: "toggle control", model: "toggle", effort: "high", models: []client.Model{{
			ID: "toggle", Capabilities: &client.ModelCapabilities{Reasoning: &client.ReasoningCapabilities{
				Supported: true, Control: client.ReasoningControlToggle,
			}},
		}}, want: "not effort"},
		{name: "unsupported effort", model: "effort", effort: "max", models: []client.Model{{
			ID: "effort", Capabilities: &client.ModelCapabilities{Reasoning: &client.ReasoningCapabilities{
				Supported: true, Control: client.ReasoningControlEffort, SupportedEfforts: []string{"low", "high"},
			}},
		}}, want: "does not support"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := config.Default()
			value.Provider.Model = "chat-model"
			value.Agents.Roles = map[string]config.AgentConfig{
				config.AgentRoleCoder: {Model: test.model, ReasoningEffort: test.effort},
			}
			if _, err := Resolve(value, config.AgentRoleCoder, test.models); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v; want containing %q", err, test.want)
			}
		})
	}
}

func TestResolveAcceptsProviderDefinedEffortWhenCatalogIsOpenEnded(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "chat-model"
	value.Agents.Roles = map[string]config.AgentConfig{
		config.AgentRoleAdvisor: {ReasoningEffort: "provider-specific"},
	}
	models := []client.Model{{
		ID: "chat-model", Capabilities: &client.ModelCapabilities{Reasoning: &client.ReasoningCapabilities{
			Supported: true, Control: client.ReasoningControlEffort,
		}},
	}}
	if _, err := Resolve(value, config.AgentRoleAdvisor, models); err != nil {
		t.Fatal(err)
	}
}

func effortCapabilities(efforts ...string) *client.ModelCapabilities {
	return &client.ModelCapabilities{Reasoning: &client.ReasoningCapabilities{
		Supported: true, Control: client.ReasoningControlEffort, SupportedEfforts: efforts,
	}}
}
