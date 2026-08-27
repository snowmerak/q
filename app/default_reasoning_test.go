package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

func TestDefaultModelReasoningPicker(t *testing.T) {
	tests := []struct {
		name, target, current, want string
		initialCursor, choice       int
		standalone                  bool
	}{
		{name: "explicit effort", target: defaultModelTarget, choice: 2, want: "high"},
		{name: "provider default", target: defaultModelTarget, current: "high", initialCursor: 2},
		{name: "retain current", target: defaultModelTarget, current: "high", initialCursor: 2, choice: 2, want: "high"},
		{name: "unsupported previous effort", target: defaultModelTarget, current: "old-effort"},
		{name: "initial setup", choice: 1, want: "low"},
		{name: "standalone settings", target: defaultModelTarget, current: "low", initialCursor: 1, choice: 2, want: "high", standalone: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := config.Store{Dir: t.TempDir()}
			value := config.Default()
			value.Provider.Model = "old-model"
			value.Provider.ReasoningEffort = test.current
			value.Agents.Roles = map[string]config.AgentConfig{
				config.AgentRolePlanner: {Model: "planner-model", ReasoningEffort: "low"},
			}
			fake := &fakeClient{}
			m := newModel(t.Context(), store, func(config.Config) (chatClient, error) { return fake, nil })
			m.enterChat(value, fake)
			m.messages = append(m.messages, client.Message{Role: client.RoleUser, Content: "keep me"})
			m.conversationID = "previous-conversation"
			m.screen, m.modelReturn = screenModels, screenChat
			if test.target == "" {
				m.modelReturn = screenSetup
			}
			m.standalone, m.standaloneRoot = test.standalone, screenModels
			m.modelTarget, m.modelPickerStage = test.target, modelPickerModels
			m.draftConfig = value
			m.models = []client.Model{{ID: "new-model", ContextLength: 123456, Capabilities: &client.ModelCapabilities{
				Reasoning: &client.ReasoningCapabilities{
					Supported: true, Control: client.ReasoningControlEffort,
					SupportedEfforts: []string{"low", "high"}, DefaultEffort: "high",
				},
			}}}

			updated, command := m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = updated.(model)
			if command != nil || m.modelPickerStage != modelPickerReasoning || m.reasoningCursor != test.initialCursor {
				t.Fatalf("stage = %v, cursor = %d, command nil = %v", m.modelPickerStage, m.reasoningCursor, command == nil)
			}
			if _, err := store.Load(); !errors.Is(err, config.ErrNotFound) {
				t.Fatalf("configuration saved before effort selection: %v", err)
			}
			if view := m.viewReasoningEfforts(); !strings.Contains(view, "default reasoning effort") || !strings.Contains(view, "omit reasoning_effort") {
				t.Fatalf("reasoning picker = %s", view)
			}
			m.reasoningCursor = test.choice
			updated, command = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = updated.(model)
			if command == nil {
				t.Fatalf("save command missing: %s", m.status)
			}
			saved, ok := command().(configuredMsg)
			if !ok || saved.err != nil {
				t.Fatalf("save result = %#v", saved)
			}
			updated, _ = m.Update(saved)
			m = updated.(model)
			loaded, err := store.Load()
			if err != nil || loaded.Provider.Model != "new-model" || loaded.Provider.ContextWindow != 123456 || loaded.Provider.ReasoningEffort != test.want {
				t.Fatalf("saved provider = %#v, err = %v", loaded.Provider, err)
			}
			if len(loaded.Agents.Roles) != 1 || loaded.Agents.Roles[config.AgentRolePlanner] != value.Agents.Roles[config.AgentRolePlanner] {
				t.Fatalf("default selection changed role settings: %#v", loaded.Agents.Roles)
			}
			if m.config.Provider.ReasoningEffort != test.want {
				t.Fatalf("active effort = %q", m.config.Provider.ReasoningEffort)
			}
			if test.standalone {
				if m.screen != screenModels || m.modelPickerStage != modelPickerTargets {
					t.Fatalf("standalone screen = %v, stage = %v", m.screen, m.modelPickerStage)
				}
			} else if m.screen != screenChat {
				t.Fatalf("screen = %v", m.screen)
			}
			if !test.standalone && test.target != "" && (len(m.messages) != 2 || m.messages[1].Content != "keep me" || m.conversationID != "") {
				t.Fatal("changing default effort should preserve history and reset provider conversation")
			}
		})
	}
}

func TestDefaultModelWithoutEffortControlsClearsPreviousEffort(t *testing.T) {
	for _, reasoning := range []*client.ReasoningCapabilities{
		nil,
		{Supported: false, Control: client.ReasoningControlEffort, SupportedEfforts: []string{"high"}},
		{Supported: true, Control: client.ReasoningControlEffort},
	} {
		store := config.Store{Dir: t.TempDir()}
		m := newModel(t.Context(), store, func(config.Config) (chatClient, error) { return &fakeClient{}, nil })
		m.draftConfig = config.Default()
		m.draftConfig.Provider.Model, m.draftConfig.Provider.ReasoningEffort = "old-model", "high"
		m.modelTarget, m.modelPickerStage = defaultModelTarget, modelPickerModels
		m.models = []client.Model{{ID: "new-model", Capabilities: &client.ModelCapabilities{Reasoning: reasoning}}}
		updated, command := m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = updated.(model)
		if command == nil || m.modelPickerStage == modelPickerReasoning {
			t.Fatalf("unexpected reasoning stage: %v", m.modelPickerStage)
		}
		result := command().(configuredMsg)
		if result.err != nil || result.config.Provider.ReasoningEffort != "" {
			t.Fatalf("save result = %#v", result)
		}
		loaded, err := store.Load()
		if err != nil || loaded.Provider.ReasoningEffort != "" {
			t.Fatalf("saved effort = %q, err = %v", loaded.Provider.ReasoningEffort, err)
		}
	}
}

func TestDefaultReasoningPickerCancelDoesNotSave(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	m := newModel(t.Context(), store, nil)
	m.config = config.Default()
	m.config.Provider.Model, m.config.Provider.ReasoningEffort = "old-model", "low"
	m.draftConfig = m.config
	m.modelChooseTarget = true
	m.modelTarget, m.modelPickerStage = defaultModelTarget, modelPickerModels
	m.models = []client.Model{{ID: "new-model", Capabilities: &client.ModelCapabilities{
		Reasoning: &client.ReasoningCapabilities{Supported: true, Control: client.ReasoningControlEffort, SupportedEfforts: []string{"high"}},
	}}}
	updated, _ := m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(model)
	if m.modelPickerStage != modelPickerModels || m.config.Provider.Model != "old-model" || m.config.Provider.ReasoningEffort != "low" {
		t.Fatalf("cancel changed active config: %#v", m.config.Provider)
	}
	updated, _ = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(model)
	if m.modelPickerStage != modelPickerTargets || m.draftConfig.Provider != m.config.Provider {
		t.Fatalf("cancel left an unsaved default model in the role settings draft: %#v", m.draftConfig.Provider)
	}
	if _, err := store.Load(); !errors.Is(err, config.ErrNotFound) {
		t.Fatalf("cancel saved configuration: %v", err)
	}
	_, command := m.saveModelTargetConfiguration(
		withAgentModel(m.draftConfig, config.AgentRolePlanner, "planner-model"), config.AgentRolePlanner,
	)
	if command == nil {
		t.Fatal("role save command is nil")
	}
	if result := command().(modelTargetConfiguredMsg); result.err != nil {
		t.Fatal(result.err)
	}
	loaded, err := store.Load()
	if err != nil || loaded.Provider != m.config.Provider {
		t.Fatalf("saving another role retained a cancelled default selection: %#v, err = %v", loaded.Provider, err)
	}
}

func TestDefaultReasoningEffortReachesChatRequests(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, withTools := range []bool{false, true} {
			for _, effort := range []string{"", "high", " \t\n"} {
				t.Run(fmt.Sprintf("stream=%v/tools=%v/effort=%q", stream, withTools, effort), func(t *testing.T) {
					value := config.Default()
					value.Provider.Model, value.Provider.ReasoningEffort = "local/model", effort
					fake := &fakeClient{}
					streaming := &scriptedStreamingClient{stream: &scriptedChatStream{chunks: []*client.ChatChunk{
						{Choices: []client.Choice{{Delta: &client.Message{Role: client.RoleAssistant, Content: "done"}}}},
					}}}
					var configuredClient chatClient = fake
					if stream {
						configuredClient = streaming
					}
					m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
					m.enterChat(value, configuredClient)
					if withTools {
						m.toolRuntime = &fakeAgentTools{}
					}
					if stream {
						m.gatewayConfig = gateway.Config{Providers: []gateway.ProviderConfig{{
							ID: "local", Prefix: "local", Type: "openai-compatible", Enabled: true,
						}}}
					}
					switch message := m.sendChatRequest()().(type) {
					case chatResultMsg:
						if message.err != nil {
							t.Fatal(message.err)
						}
					case agentEventMsg:
						if message.event.err != nil {
							t.Fatal(message.event.err)
						}
						for event := range message.events {
							if event.err != nil {
								t.Fatal(event.err)
							}
						}
					default:
						t.Fatalf("unexpected request result %T", message)
					}
					request := streaming.request
					if !stream {
						if len(fake.requests) != 1 {
							t.Fatalf("requests = %#v", fake.requests)
						}
						request = fake.requests[0]
					}
					if request.Model != value.Provider.Model || request.ReasoningEffort != strings.TrimSpace(effort) || (len(request.Tools) > 0) != withTools {
						t.Fatalf("request model = %q, effort = %q, tools = %d", request.Model, request.ReasoningEffort, len(request.Tools))
					}
				})
			}
		}
	}
}
