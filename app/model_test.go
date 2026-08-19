package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/commitagent"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/gatewayconfig"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/thinker"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
)

type fakeProviderRuntime struct {
	endpoint string
	apiKey   string
	config   gateway.Config
	applies  int
}

type extractionClient struct {
	responses  []client.Message
	requests   []client.ChatRequest
	embeddings [][]string
}

func (c *extractionClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	c.requests = append(c.requests, request)
	if len(c.responses) == 0 {
		return nil, errors.New("no extraction response")
	}
	message := c.responses[0]
	c.responses = c.responses[1:]
	return &client.ChatResponse{Choices: []client.Choice{{Message: message}}}, nil
}

func (c *extractionClient) ListModels(context.Context) ([]client.Model, error) {
	return []client.Model{{ID: "thinker-model", ContextLength: 16_000}}, nil
}

func (c *extractionClient) Close() error { return nil }

func (c *extractionClient) Embed(_ context.Context, request client.EmbeddingRequest) (*client.EmbeddingResponse, error) {
	inputs, ok := request.Input.([]string)
	if !ok {
		return nil, fmt.Errorf("embedding input = %T", request.Input)
	}
	c.embeddings = append(c.embeddings, append([]string(nil), inputs...))
	dimensions := 3
	if request.Dimensions != nil {
		dimensions = *request.Dimensions
	}
	response := &client.EmbeddingResponse{Model: request.Model, Data: make([]client.Embedding, len(inputs))}
	for index := range inputs {
		vector := make([]float64, dimensions)
		vector[0] = 1
		response.Data[index] = client.Embedding{Index: index, Embedding: vector}
	}
	return response, nil
}

func (f *fakeProviderRuntime) Endpoint() string { return f.endpoint }
func (f *fakeProviderRuntime) APIKey() string {
	if f.apiKey != "" {
		return f.apiKey
	}
	return "test-gateway-api-key"
}
func (f *fakeProviderRuntime) Config() gateway.Config { return cloneGatewayConfig(f.config) }
func (f *fakeProviderRuntime) Apply(_ context.Context, value gateway.Config) error {
	f.config = cloneGatewayConfig(value)
	f.applies++
	return nil
}

func TestManagedClientFactoryUsesGatewayAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer temporary-gateway-key" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = writer.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()

	runtime := &fakeProviderRuntime{endpoint: server.URL + "/v1", apiKey: "temporary-gateway-key"}
	factory := managedClientFactory(runtime)
	configuredClient, err := factory(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer configuredClient.Close()
	if _, err := configuredClient.ListModels(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type fakeClient struct {
	requests      []client.ChatRequest
	models        []client.Model
	listModelsErr error
	listCalls     int
	closed        bool
}

type fakeAgentTools struct {
	calls          []client.ToolCall
	loomOptions    loom.StoreOptions
	loomStats      loom.Stats
	loomCollects   int
	skills         []agentskills.Skill
	skillIssues    []agentskills.Issue
	skillReloads   int
	installedScope string
	installedRepo  string
	updatedSkill   string
	removedSkill   string
}

func (f *fakeAgentTools) Skills() []agentskills.Skill {
	return append([]agentskills.Skill(nil), f.skills...)
}
func (f *fakeAgentTools) SkillEntries() []agentskills.Skill {
	return append([]agentskills.Skill(nil), f.skills...)
}
func (f *fakeAgentTools) SkillIssues() []agentskills.Issue {
	return append([]agentskills.Issue(nil), f.skillIssues...)
}
func (f *fakeAgentTools) ReloadSkills() error { f.skillReloads++; return nil }
func (f *fakeAgentTools) InstallSkill(_ context.Context, scope, repository string) (agentskills.Skill, error) {
	f.installedScope, f.installedRepo = scope, repository
	return agentskills.Skill{Name: "installed-skill", Scope: scope}, nil
}
func (f *fakeAgentTools) UpdateSkill(_ context.Context, identifier string) (agentskills.Skill, error) {
	f.updatedSkill = identifier
	return agentskills.Skill{Name: "updated-skill", Scope: "global"}, nil
}
func (f *fakeAgentTools) RemoveSkill(_ context.Context, identifier string) (agentskills.Skill, error) {
	f.removedSkill = identifier
	return agentskills.Skill{Name: "removed-skill", Scope: "project"}, nil
}

func (f *fakeAgentTools) ConfigureLoom(options loom.StoreOptions) error {
	f.loomOptions = options
	return nil
}

func (f *fakeAgentTools) LoomStats(context.Context) (loom.Stats, error) {
	return f.loomStats, nil
}

func (f *fakeAgentTools) CollectLoom(_ context.Context, dryRun bool) (loom.GCResult, error) {
	f.loomCollects++
	return loom.GCResult{ArtifactsRemoved: 2, BytesReclaimed: 1024, DryRun: dryRun}, nil
}

func (f *fakeAgentTools) Environment() qtools.HostEnvironment {
	return qtools.HostEnvironment{OS: "test-os", Architecture: "test-arch", Shell: "test-shell"}
}

func (f *fakeAgentTools) Tools() []client.Tool {
	return []client.Tool{{
		Type: client.ToolTypeFunction,
		Function: client.FunctionDefinition{
			Name: "write_file", Description: "write a file",
			Parameters: map[string]any{"type": "object"},
		},
	}}
}

func (f *fakeAgentTools) Call(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
	f.calls = append(f.calls, call)
	return client.ToolResult{Content: `{"loom_ref":"loom://0123456789abcdef0123456789abcdef","stored":true,"result":{"path":"main.go"}}`}, nil
}

type toolCallingClient struct {
	requests []client.ChatRequest
}

type askingClient struct {
	requests []client.ChatRequest
}

type prematureCompletionClient struct {
	requests []client.ChatRequest
}

func (f *prematureCompletionClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	request.Messages = append([]client.Message(nil), request.Messages...)
	f.requests = append(f.requests, request)
	if len(f.requests) == 1 {
		return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
			Role: client.RoleAssistant,
			ToolCalls: []client.ToolCall{{
				ID: "premature-complete", Type: client.ToolTypeFunction,
				Function: client.FunctionCall{Name: taskCompleteToolName, Arguments: `{"outcome":"succeeded","summary":"Too early"}`},
			}},
		}}}}, nil
	}
	return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
		Role: client.RoleAssistant, Content: "Direct answer after correction",
	}}}}, nil
}

func (f *prematureCompletionClient) ListModels(context.Context) ([]client.Model, error) {
	return nil, nil
}
func (f *prematureCompletionClient) Close() error { return nil }

func (f *askingClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	request.Messages = append([]client.Message(nil), request.Messages...)
	f.requests = append(f.requests, request)
	if len(f.requests) == 1 {
		return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
			Role: client.RoleAssistant,
			ToolCalls: []client.ToolCall{
				{
					ID: "start-question", Type: client.ToolTypeFunction,
					Function: client.FunctionCall{Name: taskStartToolName, Arguments: `{"objective":"Choose an accent color"}`},
				},
				{
					ID: "ask-1", Type: client.ToolTypeFunction,
					Function: client.FunctionCall{
						Name:      askToUserToolName,
						Arguments: `{"question":"Which color?","context":"Choose the accent color.","choices":[{"id":"blue","label":"Blue"},{"id":"green","label":"Green"}]}`,
					},
				},
			},
		}}}}, nil
	}
	return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
		Role: client.RoleAssistant,
		ToolCalls: []client.ToolCall{{
			ID: "complete-question", Type: client.ToolTypeFunction,
			Function: client.FunctionCall{
				Name:      taskCompleteToolName,
				Arguments: `{"outcome":"succeeded","summary":"Selection recorded"}`,
			},
		}},
	}}}}, nil
}

func (f *askingClient) ListModels(context.Context) ([]client.Model, error) { return nil, nil }
func (f *askingClient) Close() error                                       { return nil }

func (f *toolCallingClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	request.Messages = append([]client.Message(nil), request.Messages...)
	f.requests = append(f.requests, request)
	if len(f.requests) == 1 {
		return &client.ChatResponse{
			ConversationID: "tool-conversation",
			Choices: []client.Choice{{Message: client.Message{
				Role: client.RoleAssistant,
				ToolCalls: []client.ToolCall{
					{
						ID: "start-1", Type: client.ToolTypeFunction,
						Function: client.FunctionCall{Name: taskStartToolName, Arguments: `{"objective":"Create a Go project"}`},
					},
					{
						ID: "call-1", Type: client.ToolTypeFunction,
						Function: client.FunctionCall{Name: "write_file", Arguments: `{"path":"main.go","content":"package main"}`},
					},
				},
			}}},
		}, nil
	}
	if len(f.requests) == 2 {
		return &client.ChatResponse{
			ConversationID: "tool-conversation",
			Choices: []client.Choice{{Message: client.Message{
				Role: client.RoleAssistant, Content: "Created main.go",
			}}},
		}, nil
	}
	return &client.ChatResponse{
		ConversationID: "tool-conversation",
		Choices: []client.Choice{{Message: client.Message{
			Role: client.RoleAssistant,
			ToolCalls: []client.ToolCall{{
				ID: "complete-1", Type: client.ToolTypeFunction,
				Function: client.FunctionCall{
					Name: taskCompleteToolName, Arguments: `{"outcome":"succeeded","summary":"Created main.go"}`,
				},
			}},
		}}},
	}, nil
}

func (f *toolCallingClient) ListModels(context.Context) ([]client.Model, error) { return nil, nil }
func (f *toolCallingClient) Close() error                                       { return nil }

func (f *fakeClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	request.Messages = append([]client.Message(nil), request.Messages...)
	f.requests = append(f.requests, request)
	n := len(f.requests)
	return &client.ChatResponse{
		ConversationID: "conversation-1",
		Choices: []client.Choice{{Message: client.Message{
			Role: client.RoleAssistant, Content: "reply " + string(rune('0'+n)),
		}}},
	}, nil
}

func (f *fakeClient) ListModels(context.Context) ([]client.Model, error) {
	f.listCalls++
	if f.listModelsErr != nil {
		return nil, f.listModelsErr
	}
	return append([]client.Model(nil), f.models...), nil
}

func (f *fakeClient) Close() error {
	f.closed = true
	return nil
}

func TestSetupMasksAndPersistsInlineKey(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	fake := &fakeClient{models: []client.Model{{ID: "z-model"}, {ID: "test-model", ContextLength: 123456}}}
	m := newModel(context.Background(), store, func(config.Config) (chatClient, error) { return fake, nil })
	m.setup[setupAPIKey].SetValue("super-secret")
	if strings.Contains(m.View().Content, "super-secret") {
		t.Fatal("setup view exposed the API key")
	}

	updated, command := m.discoverModels()
	m = updated.(model)
	modelsMessage := command()
	updated, _ = m.Update(modelsMessage)
	m = updated.(model)
	if m.screen != screenModels || len(m.models) != 2 {
		t.Fatalf("model picker state = %#v, models = %#v", m.screen, m.models)
	}
	// Models are sorted, so test-model is selected first.
	updated, command = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	message := command()
	configured, ok := message.(configuredMsg)
	if !ok || configured.err != nil || configured.client != fake {
		t.Fatalf("configured message = %#v", message)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Provider.Model != "test-model" || loaded.Provider.APIKey != "super-secret" || loaded.Provider.ContextWindow != 123456 {
		t.Fatalf("loaded config = %#v", loaded)
	}
}

func TestChatCompactsAtThresholdThenSendsPendingMessage(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	value.Provider.ContextWindow = 1000
	fake := &fakeClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, fake)
	oldUser := client.Message{Role: client.RoleUser, Content: strings.Repeat("old context ", 180)}
	oldAssistant := client.Message{Role: client.RoleAssistant, Content: strings.Repeat("old answer ", 120)}
	m.messages = append(m.messages, oldUser, oldAssistant)
	m.memory.Append(oldUser)
	m.memory.Append(oldAssistant)
	m.input.SetValue("latest request")

	updated, command := m.submitChat()
	m = updated.(model)
	if !m.compacting || command == nil {
		t.Fatalf("compacting = %v, command nil = %v", m.compacting, command == nil)
	}
	batch, ok := command().(tea.BatchMsg)
	if !ok {
		t.Fatal("compaction did not return a batch")
	}
	var compacted compactionResultMsg
	found := false
	for _, child := range batch {
		if message, ok := child().(compactionResultMsg); ok {
			compacted = message
			found = true
		}
	}
	if !found || len(fake.requests) != 1 || fake.requests[0].MaxCompletionTokens == nil {
		t.Fatalf("compaction request = %#v", fake.requests)
	}

	updated, command = m.Update(compacted)
	m = updated.(model)
	if m.compacting || command == nil {
		t.Fatalf("post-compaction state = compacting %v, command nil %v", m.compacting, command == nil)
	}
	result, ok := command().(chatResultMsg)
	if !ok {
		t.Fatal("compaction did not continue the pending chat")
	}
	updated, _ = m.Update(result)
	m = updated.(model)
	if len(fake.requests) != 2 || fake.requests[1].ConversationID != "" {
		t.Fatalf("requests = %#v", fake.requests)
	}
	requestMessages := fake.requests[1].Messages
	if requestMessages[len(requestMessages)-1].Content != "latest request" {
		t.Fatalf("pending request was not preserved: %#v", requestMessages)
	}
	if len(m.messages) != 5 { // system, two old messages, latest user, assistant reply
		t.Fatalf("full transcript was not preserved: %#v", m.messages)
	}
}

func TestEnterSendsAndShiftEnterAddsNewline(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	fake := &fakeClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, fake)
	m.input.SetValue("first line")

	updated, _ := m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	m = updated.(model)
	if !strings.Contains(m.input.Value(), "\n") || len(fake.requests) != 0 {
		t.Fatalf("shift+enter value = %q, requests = %d", m.input.Value(), len(fake.requests))
	}

	m.input.SetValue("send me")
	updated, delayCommand := m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if !m.submitPending || delayCommand == nil || m.waiting {
		t.Fatal("enter did not schedule chat submission")
	}
	updated, command := m.Update(deferredSubmitMsg{})
	m = updated.(model)
	if !m.waiting || command == nil {
		t.Fatal("deferred enter did not submit chat")
	}
}

func TestUnicodeInputUsesNFCAndRealCursor(t *testing.T) {
	decomposed := "\u1100\u1161" // Hangul choseong kiyeok + jungseong a.
	key := normalizeTextInputMessage(tea.KeyPressMsg{Code: 'ᄀ', Text: decomposed}).(tea.KeyPressMsg)
	if key.Text != "가" {
		t.Fatalf("normalized key text = %q", key.Text)
	}
	paste := normalizeTextInputMessage(tea.PasteMsg{Content: decomposed}).(tea.PasteMsg)
	if paste.Content != "가" {
		t.Fatalf("normalized paste = %q", paste.Content)
	}

	value := config.Default()
	value.Provider.Model = "test-model"
	fake := &fakeClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, fake)
	assistant := client.Message{Role: client.RoleAssistant, Content: "**알겠어요.** 필요한 때 말해 주세요."}
	m.messages = append(m.messages, assistant)
	m.memory.Append(assistant)
	m.refreshTranscript()
	view := m.View()
	if m.input.VirtualCursor() || view.Cursor == nil || view.Cursor.Shape != tea.CursorBar {
		t.Fatal("chat input is not using the real terminal cursor")
	}
	inputRow := -1
	for row, line := range strings.Split(ansi.Strip(view.Content), "\n") {
		if strings.Contains(line, "Type a message") {
			inputRow = row
			break
		}
	}
	if inputRow < 0 || view.Cursor.Position.Y != inputRow {
		t.Fatalf("cursor row = %d, input row = %d", view.Cursor.Position.Y, inputRow)
	}
	m.messages = m.messages[:1]
	m.memory.Reset(m.messages)
	m.refreshTranscript()
	m = submitAndReceive(t, m, decomposed)
	if got := fake.requests[0].Messages[1].Content; got != "가" {
		t.Fatalf("submitted content = %q", got)
	}
}

func TestEnterWaitsForFinalIMECommitBeforeSending(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	fake := &fakeClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, fake)
	m.input.SetValue("마지막 글")

	updated, command := m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if !m.submitPending || command == nil || len(fake.requests) != 0 {
		t.Fatalf("pending = %v, command nil = %v, requests = %d", m.submitPending, command == nil, len(fake.requests))
	}

	// Windows IMEs can deliver the committed composition after the Enter key.
	updated, _ = m.Update(tea.KeyPressMsg{Code: '자', Text: "자"})
	m = updated.(model)
	updated, sendCommand := m.Update(deferredSubmitMsg{})
	m = updated.(model)
	if m.submitPending || !m.waiting || sendCommand == nil {
		t.Fatalf("pending = %v, waiting = %v, command nil = %v", m.submitPending, m.waiting, sendCommand == nil)
	}
	result, ok := sendCommand().(tea.BatchMsg)
	if !ok {
		t.Fatal("deferred submit did not start the chat request")
	}
	for _, child := range result {
		_ = child()
	}
	if len(fake.requests) != 1 {
		t.Fatalf("requests = %d", len(fake.requests))
	}
	if got := fake.requests[0].Messages[1].Content; got != "마지막 글자" {
		t.Fatalf("submitted content = %q", got)
	}
	if m.input.Value() != "" {
		t.Fatalf("input retained final IME character: %q", m.input.Value())
	}
}

func TestSlashModelOpensPickerAndPreservesHistory(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "old-model"
	fake := &fakeClient{models: []client.Model{{ID: "old-model"}, {ID: "new-model"}}}
	m := newModel(context.Background(), store, func(config.Config) (chatClient, error) { return fake, nil })
	m.enterChat(value, fake)
	m.messages = append(m.messages, client.Message{Role: client.RoleUser, Content: "keep me"})
	m.input.SetValue("/model")

	updated, _ := m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, command := m.Update(deferredSubmitMsg{})
	m = updated.(model)
	if m.screen != screenModels || !m.discovering || command == nil {
		t.Fatalf("model discovery state = screen %v, discovering %v", m.screen, m.discovering)
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if m.screen != screenModels || len(m.models) != 2 {
		t.Fatalf("model picker state = screen %v, models %#v", m.screen, m.models)
	}
	if m.modelPickerStage != modelPickerTargets {
		t.Fatalf("model picker stage = %v", m.modelPickerStage)
	}
	updated, _ = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.modelPickerStage != modelPickerModels || m.modelTarget != defaultModelTarget {
		t.Fatalf("default target state = stage %v, target %q", m.modelPickerStage, m.modelTarget)
	}
	m.modelCursor = 0 // new-model sorts before old-model.
	updated, command = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(command())
	m = updated.(model)
	if m.screen != screenChat || m.config.Provider.Model != "new-model" {
		t.Fatalf("selected model = %q on screen %v", m.config.Provider.Model, m.screen)
	}
	loaded, err := store.Load()
	if err != nil || loaded.Provider.Model != "new-model" {
		t.Fatalf("global model config = %#v, err = %v", loaded, err)
	}
	if len(m.messages) != 2 || m.messages[1].Content != "keep me" {
		t.Fatalf("history was not preserved: %#v", m.messages)
	}
}

func TestSlashModelConfiguresCommitAgentModelAndReasoning(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "default-model"
	fake := &fakeClient{models: []client.Model{
		{ID: "default-model"},
		{ID: "commit-model", Capabilities: &client.ModelCapabilities{Reasoning: &client.ReasoningCapabilities{
			Supported: true, Control: client.ReasoningControlEffort,
			SupportedEfforts: []string{"low", "high"}, DefaultEffort: "high",
		}}},
	}}
	m := newModel(context.Background(), store, nil)
	m.enterChat(value, fake)
	m.messages = append(m.messages, client.Message{Role: client.RoleUser, Content: "keep me"})
	m.input.SetValue("/model")

	updated, _ := m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, command := m.Update(deferredSubmitMsg{})
	m = updated.(model)
	updated, _ = m.Update(command())
	m = updated.(model)

	targets := modelTargets()
	for index, target := range targets {
		if target == config.AgentRoleCommit {
			m.modelTargetCursor = index
			break
		}
	}
	updated, _ = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.modelPickerStage != modelPickerModels || m.modelTarget != config.AgentRoleCommit {
		t.Fatalf("commit target state = stage %v, target %q", m.modelPickerStage, m.modelTarget)
	}
	for index, model := range m.filteredModels() {
		if model.ID == "commit-model" {
			m.modelCursor = index
			break
		}
	}
	updated, command = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if command != nil || m.modelPickerStage != modelPickerReasoning {
		t.Fatalf("reasoning stage = %v, command nil = %v", m.modelPickerStage, command == nil)
	}
	m.reasoningCursor = 2 // default, low, high
	updated, command = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if command == nil {
		t.Fatal("save subagent configuration command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)

	agent := m.config.Agents.Roles[config.AgentRoleCommit]
	if m.screen != screenModels || m.modelPickerStage != modelPickerTargets ||
		agent.Model != "commit-model" || agent.ReasoningEffort != "high" {
		t.Fatalf("saved commit config = %#v on screen %v", agent, m.screen)
	}
	if targets[m.modelTargetCursor] != config.AgentRoleCommit {
		t.Fatalf("target cursor moved to %q", targets[m.modelTargetCursor])
	}
	if fake.closed {
		t.Fatal("subagent model change replaced the main chat client")
	}
	if len(m.messages) != 2 || m.messages[1].Content != "keep me" {
		t.Fatalf("history was not preserved: %#v", m.messages)
	}
	loaded, err := store.Load()
	if err != nil || loaded.Agents.Roles[config.AgentRoleCommit] != agent {
		t.Fatalf("loaded config = %#v, err = %v", loaded, err)
	}
	updated, _ = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(model)
	if m.screen != screenChat {
		t.Fatalf("escape returned to screen %v", m.screen)
	}
}

func TestModelTargetsIncludeLearningRoles(t *testing.T) {
	for _, expected := range []string{config.AgentRoleThinker, config.AgentRoleLibrarian} {
		found := false
		for _, target := range modelTargets() {
			if target == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("model targets = %#v; missing %q", modelTargets(), expected)
		}
	}
}

func TestExplicitLearningSegmentStartsThinkerExtractionThroughLibraryClient(t *testing.T) {
	var registered qlibrary.PropositionRegisterRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/propositions" || !strings.HasPrefix(request.Header.Get("Idempotency-Key"), "learn-") || !strings.HasSuffix(request.Header.Get("Idempotency-Key"), "/0") {
			t.Errorf("registration request = %s %s, key %q", request.Method, request.URL.Path, request.Header.Get("Idempotency-Key"))
		}
		if err := json.NewDecoder(request.Body).Decode(&registered); err != nil {
			t.Error(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"prop-app-test","created":true}`))
	}))
	defer server.Close()
	configuredClient := &extractionClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
			ID: "register", Type: client.ToolTypeFunction,
			Function: client.FunctionCall{Name: thinker.RegisterToolName, Arguments: `{"content":"Use one proposition per call.","queries":["proposition call size"],"confidence":0.9,"tags":["thinker"]}`},
		}}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
			ID: "complete", Type: client.ToolTypeFunction,
			Function: client.FunctionCall{Name: thinker.CompleteToolName, Arguments: `{}`},
		}}},
	}}
	value := config.Default()
	value.Provider.Model = "thinker-model"
	value.Embedding = config.EmbeddingConfig{Model: "embed-model", Dimensions: 3}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.config = value
	m.client = configuredClient
	m.libraryClient = qlibrary.NewClient(server.URL+"/v1", "test-key", time.Second)
	if err := m.libraryClient.ConfigureEmbedding(configuredClient, value.Embedding.Model, value.Embedding.Dimensions); err != nil {
		t.Fatal(err)
	}
	m.models = []client.Model{{ID: "thinker-model", ContextLength: 16_000}}
	m.messages = []client.Message{
		{Role: client.RoleSystem, Content: "main prompt"},
		{Role: client.RoleUser, Content: "Remember this decision."},
		{Role: client.RoleAssistant, Content: "Understood."},
	}
	m.runID = "run-test"
	if err := m.ensureLearningMachine(thinker.LearningState{}, nil); err != nil {
		t.Fatal(err)
	}
	m.learning.AppendMessage(m.messages[1])
	m.learning.AppendMessage(m.messages[2])
	m.learning.EnqueueExplicit()
	command := m.startNextLearningSegment()
	if command == nil {
		t.Fatal("explicit segment did not start Thinker extraction")
	}
	message, ok := command().(thinkerResultMsg)
	if !ok || message.err != nil || message.result.Registered != 1 {
		t.Fatalf("Thinker result = %#v", message)
	}
	if registered.Content != "Use one proposition per call." || registered.ExtractorModel != "thinker-model" || registered.ExtractorVersion != thinker.ExtractorVersion ||
		registered.Embeddings == nil || len(registered.Embeddings.Vectors) != 2 || len(configuredClient.embeddings) != 1 {
		t.Fatalf("registered proposition = %#v", registered)
	}
}

func TestModelPickerConfiguresEmbeddingModelAndDimensions(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "default-model"
	fake := &fakeClient{}
	m := newModel(context.Background(), store, nil)
	m.enterChat(value, fake)
	m.messages = append(m.messages, client.Message{Role: client.RoleUser, Content: "keep me"})
	m.modelReturn = screenChat
	m.modelChooseTarget = true
	m.enterModelPicker(value, []client.Model{{ID: "default-model"}, {ID: "embed-model"}})

	targets := modelTargets()
	for index, target := range targets {
		if target == embeddingModelTarget {
			m.modelTargetCursor = index
			break
		}
	}
	updated, _ := m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.modelPickerStage != modelPickerModels || m.modelTarget != embeddingModelTarget {
		t.Fatalf("embedding target state = stage %v, target %q", m.modelPickerStage, m.modelTarget)
	}
	m.modelCursor = 1 // default-model sorts before embed-model.
	updated, command := m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if command == nil || m.modelPickerStage != modelPickerEmbeddingDimensions {
		t.Fatalf("dimension stage = %v, command nil = %v", m.modelPickerStage, command == nil)
	}
	m.embeddingDimensions.SetValue("0")
	updated, command = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if command != nil || !strings.Contains(m.status, "between 1 and 4096") {
		t.Fatalf("invalid dimension state = command %v, status %q", command, m.status)
	}
	m.embeddingDimensions.SetValue("1536")
	updated, command = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if command == nil {
		t.Fatal("save embedding configuration command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if m.screen != screenModels || m.modelPickerStage != modelPickerTargets ||
		m.config.Embedding.Model != "embed-model" || m.config.Embedding.Dimensions != 1536 {
		t.Fatalf("saved embedding config = %#v on screen %v stage %v", m.config.Embedding, m.screen, m.modelPickerStage)
	}
	if targets[m.modelTargetCursor] != embeddingModelTarget {
		t.Fatalf("target cursor moved to %q", targets[m.modelTargetCursor])
	}
	if fake.closed || len(m.messages) != 2 || m.messages[1].Content != "keep me" {
		t.Fatalf("embedding config disturbed chat: closed %v, messages %#v", fake.closed, m.messages)
	}
	loaded, err := store.Load()
	if err != nil || loaded.Embedding != m.config.Embedding {
		t.Fatalf("loaded config = %#v, err = %v", loaded, err)
	}
}

func TestModelPickerCanClearEmbeddingConfiguration(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "default-model"
	value.Embedding = config.EmbeddingConfig{Model: "embed-model", Dimensions: 3072}
	m := newModel(context.Background(), store, nil)
	m.enterChat(value, &fakeClient{})
	m.modelReturn = screenChat
	m.modelChooseTarget = true
	m.enterModelPicker(value, []client.Model{{ID: "default-model"}, {ID: "embed-model"}})
	for index, target := range modelTargets() {
		if target == embeddingModelTarget {
			m.modelTargetCursor = index
			break
		}
	}
	updated, command := m.updateModelPicker(tea.KeyPressMsg{Code: 'i', Text: "i"})
	m = updated.(model)
	if command == nil {
		t.Fatal("clear embedding command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if m.config.Embedding != (config.EmbeddingConfig{}) || m.modelPickerStage != modelPickerTargets {
		t.Fatalf("embedding config was not cleared: %#v", m.config.Embedding)
	}
}

func TestModelPickerCanRestoreSubagentDefaultInheritance(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "default-model"
	value.Agents.Roles = map[string]config.AgentConfig{
		config.AgentRoleCoder: {Model: "coding-model", ReasoningEffort: "high"},
	}
	m := newModel(context.Background(), store, nil)
	m.enterChat(value, &fakeClient{})
	m.modelReturn = screenChat
	m.modelChooseTarget = true
	m.enterModelPicker(value, []client.Model{{ID: "default-model"}, {ID: "coding-model"}})
	for index, target := range modelTargets() {
		if target == config.AgentRoleCoder {
			m.modelTargetCursor = index
			break
		}
	}
	updated, command := m.updateModelPicker(tea.KeyPressMsg{Code: 'i', Text: "i"})
	m = updated.(model)
	if command == nil {
		t.Fatal("inherit default command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if _, configured := m.config.Agents.Roles[config.AgentRoleCoder]; configured {
		t.Fatalf("coder override was not removed: %#v", m.config.Agents.Roles)
	}
	if m.screen != screenModels || m.modelPickerStage != modelPickerTargets {
		t.Fatalf("inherit returned to screen %v stage %v", m.screen, m.modelPickerStage)
	}
}

func TestAgentModelSelectionReplacesGroupAndSummaryShowsGroup(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "default-model"
	value.ModelGroups = map[string]config.ModelGroupConfig{
		"heavy": {Candidates: []config.ModelCandidateConfig{{Model: "primary"}, {Model: "secondary"}}},
	}
	value.Agents.Roles = map[string]config.AgentConfig{
		config.AgentRolePlanner: {Group: "heavy", ReasoningEffort: "high"},
	}
	if summary := targetModelSummary(value, config.AgentRolePlanner); summary != "group heavy · 2 candidates · effort high" {
		t.Fatalf("summary = %q", summary)
	}
	updated := withAgentModel(value, config.AgentRolePlanner, "single-model")
	agent := updated.Agents.Roles[config.AgentRolePlanner]
	if agent.Model != "single-model" || agent.Group != "" || agent.ReasoningEffort != "high" {
		t.Fatalf("agent = %#v", agent)
	}
	m := model{
		draftConfig: value, modelTarget: config.AgentRolePlanner,
		models: []client.Model{{ID: "primary"}, {ID: "secondary"}},
	}
	choices := m.selectableModels()
	if len(choices) != 3 || choices[2].ID != "group:heavy" {
		t.Fatalf("choices = %#v", choices)
	}
	grouped := withAgentGroup(updated, config.AgentRolePlanner, "heavy")
	agent = grouped.Agents.Roles[config.AgentRolePlanner]
	if agent.Model != "" || agent.Group != "heavy" {
		t.Fatalf("grouped agent = %#v", agent)
	}
}

func TestModelGroupsTUIShortcutCreatesOrderedGroup(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "primary"
	value.Agents.Roles = map[string]config.AgentConfig{
		config.AgentRolePlanner: {Model: "primary", ReasoningEffort: "high"},
	}
	m := newModel(context.Background(), store, nil)
	m.draftConfig = value
	m.config = value
	m.screen = screenModels
	m.modelPickerStage = modelPickerTargets
	m.models = []client.Model{
		{ID: "primary", ContextLength: 200_000, MaxOutputTokens: 16_000, Capabilities: &client.ModelCapabilities{Reasoning: &client.ReasoningCapabilities{
			Supported: true, Control: client.ReasoningControlEffort, SupportedEfforts: []string{"high"},
		}}},
		{ID: "secondary", ContextLength: 128_000, MaxOutputTokens: 8_000, Capabilities: &client.ModelCapabilities{Reasoning: &client.ReasoningCapabilities{
			Supported: true, Control: client.ReasoningControlEffort, SupportedEfforts: []string{"high"},
		}}},
	}

	updated, _ := m.updateModelPicker(tea.KeyPressMsg{Code: 'g', Text: "g"})
	m = updated.(model)
	if m.modelPickerStage != modelPickerGroups {
		t.Fatalf("g stage = %v", m.modelPickerStage)
	}
	updated, _ = m.updateModelPicker(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = updated.(model)
	m.modelGroupNameInput.SetValue("heavy")
	updated, _ = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.modelPickerStage != modelPickerGroupCandidates {
		t.Fatalf("name stage = %v", m.modelPickerStage)
	}

	addCandidate := func(modelIndex int, timeout string) {
		updated, _ = m.updateModelPicker(tea.KeyPressMsg{Code: 'a', Text: "a"})
		m = updated.(model)
		for m.modelCursor < modelIndex {
			updated, _ = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyDown})
			m = updated.(model)
		}
		if m.modelCursor != modelIndex {
			t.Fatalf("arrow navigation cursor = %d, want %d", m.modelCursor, modelIndex)
		}
		if modelIndex > 0 {
			updated, _ = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyUp})
			m = updated.(model)
			if m.modelCursor != modelIndex-1 {
				t.Fatalf("up arrow cursor = %d, want %d", m.modelCursor, modelIndex-1)
			}
			updated, _ = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyDown})
			m = updated.(model)
		}
		updated, _ = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = updated.(model)
		if m.modelPickerStage != modelPickerGroupCandidateReasoning {
			t.Fatalf("model stage = %v", m.modelPickerStage)
		}
		m.reasoningCursor = 1
		updated, _ = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = updated.(model)
		m.modelGroupTimeoutInput.SetValue(timeout)
		updated, _ = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = updated.(model)
		if m.modelPickerStage != modelPickerGroupCandidates {
			t.Fatalf("timeout stage = %v", m.modelPickerStage)
		}
	}
	addCandidate(0, "60s")
	addCandidate(1, "")
	updated, command := m.updateModelPicker(tea.KeyPressMsg{Code: 's', Text: "s"})
	m = updated.(model)
	if command == nil {
		t.Fatal("save group command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	group := m.config.ModelGroups["heavy"]
	if m.modelPickerStage != modelPickerGroups || len(group.Candidates) != 2 ||
		group.Candidates[0].Model != "primary" || group.Candidates[0].Timeout != time.Minute ||
		group.Candidates[1].Model != "secondary" {
		t.Fatalf("saved group = %#v, stage = %v", group, m.modelPickerStage)
	}
	if contextLength, output := modelGroupLimits(group, m.models); contextLength != 128_000 || output != 8_000 {
		t.Fatalf("limits = %d/%d", contextLength, output)
	}
	planner := m.config.Agents.Roles[config.AgentRolePlanner]
	if planner.Model != "primary" || planner.Group != "" || planner.ReasoningEffort != "high" {
		t.Fatalf("saving group changed planner = %#v", planner)
	}
}

func TestModelGroupCatalogKeepsAvailableGroupWithUnknownLimits(t *testing.T) {
	group := config.ModelGroupConfig{Candidates: []config.ModelCandidateConfig{
		{Model: "known"}, {Model: "unknown-limits"},
	}}
	models := []client.Model{
		{ID: "known", ContextLength: 128_000, MaxOutputTokens: 8_000},
		{ID: "unknown-limits"},
	}
	if !modelGroupAvailable(group, models) {
		t.Fatal("available group was rejected")
	}
	if contextLength, output := modelGroupLimits(group, models); contextLength != 0 || output != 0 {
		t.Fatalf("unknown limits must stay unknown, got %d/%d", contextLength, output)
	}
	m := model{draftConfig: config.Config{ModelGroups: map[string]config.ModelGroupConfig{"heavy": group}}, models: models}
	m.refreshModelGroupCatalog()
	found := false
	for _, model := range m.models {
		if model.ID == "group/heavy" {
			found = true
			if model.ContextLength != 0 || model.MaxOutputTokens != 0 {
				t.Fatalf("synthetic group = %#v", model)
			}
		}
	}
	if !found {
		t.Fatal("available group with unknown limits was not exposed")
	}
}

func TestSlashGatewayOpensSetupWithoutManagedRuntime(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	fake := &fakeClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, fake)
	m.input.SetValue("/gateway")

	updated, _ := m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(deferredSubmitMsg{})
	m = updated.(model)
	if m.screen != screenSetup || !m.setupEdit || m.input.Value() != "" {
		t.Fatalf("Gateway command state = screen %v, edit %v, input %q", m.screen, m.setupEdit, m.input.Value())
	}
}

func TestSlashLoomEditsSettingsAndPreviewsGC(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test-model"
	tools := &fakeAgentTools{loomStats: loom.Stats{Artifacts: 4, Blobs: 3, Bytes: 12 << 20}}
	m := newModel(context.Background(), store, nil)
	m.toolRuntime = tools
	m.enterChat(value, &fakeClient{})
	m.input.SetValue("/loom")

	updated, _ := m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, command := m.Update(deferredSubmitMsg{})
	m = updated.(model)
	if m.screen != screenLoom || command == nil {
		t.Fatalf("Loom screen state = %v, command nil = %v", m.screen, command == nil)
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if m.loomStats.Bytes != 12<<20 || !strings.Contains(m.View().Content, "12.0 MiB / 256.0 MiB") {
		t.Fatalf("Loom stats = %#v, view = %q", m.loomStats, m.View().Content)
	}
	m.loomInputs[0].SetValue("32")
	m.loomInputs[1].SetValue("128")
	m.loomInputs[2].SetValue("75")
	m.loomInputs[3].SetValue("50")
	m.loomInputs[4].SetValue("48")
	m.loomFocus = loomDryRun
	updated, command = m.updateLoom(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if command == nil || !m.loomBusy {
		t.Fatalf("Loom action command nil = %v, busy = %v", command == nil, m.loomBusy)
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Loom.MaximumArtifactMiB != 32 || loaded.Loom.MaximumStoreMiB != 128 ||
		loaded.Loom.GC.TriggerRatio != .75 || loaded.Loom.GC.TargetRatio != .50 || loaded.Loom.GC.GraceHours != 48 {
		t.Fatalf("saved Loom config = %#v", loaded.Loom)
	}
	if tools.loomCollects != 1 || tools.loomOptions.MaximumStoreBytes != 128<<20 || !strings.Contains(m.status, "GC preview") {
		t.Fatalf("Loom action = collects %d, options %#v, status %q", tools.loomCollects, tools.loomOptions, m.status)
	}
}

func TestSlashIgnoreEditsAndSavesWorkspaceRules(t *testing.T) {
	root := t.TempDir()
	workspaceStore := workspace.Store{Root: root}
	if err := workspaceStore.SaveIgnore(".gradle/\n"); err != nil {
		t.Fatal(err)
	}
	value := config.Default()
	value.Provider.Model = "test-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspaceStore
	m.enterChat(value, &fakeClient{})
	m.resize(100, 32)
	m.input.SetValue("/ignore")

	updated, _ := m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(deferredSubmitMsg{})
	m = updated.(model)
	if m.screen != screenIgnore || m.ignoreEditor.Value() != ".gradle/\n" {
		t.Fatalf("ignore screen = %v, content %q", m.screen, m.ignoreEditor.Value())
	}
	view := ansi.Strip(m.View().Content)
	for _, expected := range []string{"q · Discovery ignore", ".q/ is always excluded", "DISCOVERY PATTERNS", "ctrl+s save"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("ignore view missing %q:\n%s", expected, view)
		}
	}
	if height := lipgloss.Height(view); height > m.height {
		t.Fatalf("ignore view height %d exceeds terminal %d:\n%s", height, m.height, view)
	}

	m.ignoreEditor.SetValue(".gradle/\nbuild/\nvendor/")
	updated, _ = m.updateIgnore(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = updated.(model)
	saved, err := workspaceStore.LoadIgnore()
	if err != nil {
		t.Fatal(err)
	}
	if saved != ".gradle/\nbuild/\nvendor/\n" || !strings.Contains(m.status, "saved") {
		t.Fatalf("saved ignore = %q, status %q", saved, m.status)
	}

	m.ignoreEditor.SetValue(saved + "tmp/\n")
	updated, _ = m.updateIgnore(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(model)
	if m.screen != screenIgnore || !m.ignoreDiscardArmed {
		t.Fatalf("dirty escape left screen: screen %v armed %v", m.screen, m.ignoreDiscardArmed)
	}
	updated, _ = m.updateIgnore(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(model)
	if m.screen != screenChat {
		t.Fatalf("second escape screen = %v", m.screen)
	}
}

func TestHelpCommandAndShortcutKeepCommandsOutOfChatFooter(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, &fakeClient{})
	m.resize(96, 24)

	chat := ansi.Strip(m.viewChat())
	if !strings.Contains(chat, "ctrl+h help") {
		t.Fatalf("chat footer does not advertise help:\n%s", chat)
	}
	for _, command := range []string{"/clear", "/learn", "/commit", "/model", "/gateway", "/loom", "/ignore", "/skills", "/mcp", "/help"} {
		if strings.Contains(chat, command) {
			t.Fatalf("chat footer exposes %q:\n%s", command, chat)
		}
	}

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	m = updated.(model)
	if m.screen != screenHelp || m.helpReturn != screenChat {
		t.Fatalf("ctrl+h screen = %v, return = %v", m.screen, m.helpReturn)
	}
	help := ansi.Strip(m.View().Content)
	for _, expected := range []string{
		"q · Help", "SLASH COMMANDS", "/plan [request]", "/commit", "/clear", "/learn", "/model",
		"/gateway", "/loom", "/ignore", "/skills",
	} {
		if !strings.Contains(help, expected) {
			t.Fatalf("help view missing %q:\n%s", expected, help)
		}
	}
	allHelp := ansi.Strip(renderHelpContent(m.dark))
	for _, expected := range []string{
		"/help", "/mcp", "SUBAGENTS AND QUESTIONS", "ctrl+g", "COMMAND LINE", "q commit", "q gateway config",
		"q model", "q mcp", "q skills", "q ignore", "q help",
	} {
		if !strings.Contains(allHelp, expected) {
			t.Fatalf("help content missing %q:\n%s", expected, allHelp)
		}
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	m = updated.(model)
	if m.screen != screenChat {
		t.Fatalf("second ctrl+h screen = %v", m.screen)
	}

	m.input.SetValue("/help")
	updated, _ = m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(deferredSubmitMsg{})
	m = updated.(model)
	if m.screen != screenHelp {
		t.Fatalf("/help screen = %v", m.screen)
	}
	updated, _ = m.updateHelp(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(model)
	if m.screen != screenChat {
		t.Fatalf("help escape screen = %v", m.screen)
	}
}

func TestSlashSkillsShowsCatalogAndReloads(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	tools := &fakeAgentTools{
		skills: []agentskills.Skill{{
			Name: "review-go", Description: "Review Go changes.", Source: agentskills.SourceProjectPortable,
			Scope: "project", Directory: filepath.Join(t.TempDir(), ".agents", "skills", "review-go"),
		}},
		skillIssues: []agentskills.Issue{{Path: "bad-skill", Message: "invalid frontmatter"}},
	}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.toolRuntime = tools
	m.enterChat(value, &fakeClient{})
	m.resize(96, 24)
	m.input.SetValue("/skills")
	updated, _ := m.submitChat()
	m = updated.(model)
	if m.screen != screenSkills {
		t.Fatalf("screen = %v", m.screen)
	}
	view := ansi.Strip(m.View().Content)
	for _, expected := range []string{"q · Agent Skills", "review-go", "Review Go changes", "DISCOVERY NOTES", "invalid frontmatter"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("skills view missing %q:\n%s", expected, view)
		}
	}
	updated, command := m.Update(tea.KeyPressMsg{Code: 'r'})
	m = updated.(model)
	if command == nil || !m.skillsBusy {
		t.Fatal("reload did not start asynchronously")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if tools.skillReloads != 1 || !strings.Contains(m.status, "1 active") {
		t.Fatalf("reloads=%d status=%q", tools.skillReloads, m.status)
	}
}

func TestSlashLearnAcknowledgesEmptyAndClosesEligibleContext(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	workspaceStore := workspace.Store{Root: t.TempDir()}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspaceStore
	m.enterChat(value, &fakeClient{})

	m.input.SetValue("/learn")
	updated, _ := m.submitChat()
	m = updated.(model)
	if m.status != "Learning checkpoint enqueued" {
		t.Fatalf("empty /learn status = %q", m.status)
	}
	if _, _, ok := m.learning.Next(); ok {
		t.Fatal("empty /learn created a learning segment")
	}

	m.learning.AppendMessage(client.Message{Role: client.RoleUser, Content: "Prefer checkpointed learning."})
	m.input.SetValue("/learn")
	updated, _ = m.submitChat()
	m = updated.(model)
	segment, messages, ok := m.learning.Next()
	if !ok || segment.Reason != thinker.BoundaryExplicit || len(messages) != 1 ||
		messages[0].Content != "Prefer checkpointed learning." {
		t.Fatalf("explicit learning segment = %#v, %#v", segment, messages)
	}
}

func TestSlashSkillsGitManagement(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	tools := &fakeAgentTools{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.toolRuntime = tools
	m.enterChat(value, &fakeClient{})
	m.input.SetValue("/skills add global https://example.test/skill.git")
	updated, command := m.submitChat()
	m = updated.(model)
	if command == nil || !m.skillsBusy {
		t.Fatalf("command=%v busy=%v", command, m.skillsBusy)
	}
	message := command()
	updated, _ = m.Update(message)
	m = updated.(model)
	if tools.installedScope != "global" || tools.installedRepo != "https://example.test/skill.git" || !strings.Contains(m.status, "Installed installed-skill") {
		t.Fatalf("scope=%q repo=%q status=%q", tools.installedScope, tools.installedRepo, m.status)
	}
}

func TestSkillsScreenManagesFocusedScope(t *testing.T) {
	globalDir := filepath.Join(t.TempDir(), ".q", "skills", "global-skill")
	projectDir := filepath.Join(t.TempDir(), ".q", "skills", "project-skill")
	for _, directory := range []string{globalDir, projectDir} {
		if err := os.MkdirAll(filepath.Join(directory, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tools := &fakeAgentTools{skills: []agentskills.Skill{
		{ID: "skill-global", Name: "global-skill", Description: "Global instructions.", Scope: "global", Source: agentskills.SourceUserQ, Directory: globalDir},
		{ID: "skill-project", Name: "project-skill", Description: "Workspace instructions.", Scope: "project", Source: agentskills.SourceProjectQ, Directory: projectDir},
	}}
	value := config.Default()
	value.Provider.Model = "test-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.toolRuntime = tools
	m.enterChat(value, &fakeClient{})
	m.resize(110, 28)
	updated, _ := m.enterSkills()
	m = updated.(model)

	view := ansi.Strip(m.View().Content)
	for _, expected := range []string{"GLOBAL SKILLS · 1", "SESSION SKILLS · 1", "global-skill", "project-skill", "q-managed Git"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("skills manager missing %q:\n%s", expected, view)
		}
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'a'})
	m = updated.(model)
	if m.skillsMode != skillModeAdd {
		t.Fatalf("add mode = %v", m.skillsMode)
	}
	m.skillsInput.SetValue("https://example.test/global.git")
	updated, command := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if command == nil || !m.skillsBusy {
		t.Fatal("interactive install did not start")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if tools.installedScope != "global" || tools.installedRepo != "https://example.test/global.git" {
		t.Fatalf("install scope=%q repository=%q", tools.installedScope, tools.installedRepo)
	}

	updated, command = m.Update(tea.KeyPressMsg{Code: 'u'})
	m = updated.(model)
	if command == nil {
		t.Fatal("interactive pull did not start")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if tools.updatedSkill != "skill-global" {
		t.Fatalf("updated skill = %q", tools.updatedSkill)
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'd'})
	m = updated.(model)
	if m.skillsMode != skillModeRemove {
		t.Fatalf("remove mode = %v", m.skillsMode)
	}
	updated, command = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if command == nil {
		t.Fatal("interactive removal did not start")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if tools.removedSkill != "skill-global" {
		t.Fatalf("removed skill = %q", tools.removedSkill)
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'a'})
	m = updated.(model)
	m.skillsInput.SetValue("https://example.test/project.git")
	updated, command = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(command())
	m = updated.(model)
	if tools.installedScope != "project" {
		t.Fatalf("session panel installed to %q", tools.installedScope)
	}
}

func TestSlashCommitStartsEmbeddedWorkflowAndRestoresChatStatus(t *testing.T) {
	root := t.TempDir()
	workspaceStore := workspace.Store{Root: root}
	workspaceLock, err := workspace.AcquireLock(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceLock.Close() })

	value := config.Default()
	value.Provider.Model = "test-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspaceStore
	m.workspaceLock = workspaceLock
	m.enterChat(value, &fakeClient{})
	m.input.SetValue("/commit")

	updated, command := m.submitChat()
	m = updated.(model)
	if command == nil || !m.commitRunning {
		t.Fatalf("commit start = command %v, running %v", command != nil, m.commitRunning)
	}
	if value := m.input.Value(); value != "" {
		t.Fatalf("commit command left input = %q", value)
	}

	updated, _ = m.Update(commitFinishedMsg{result: commitagent.Result{
		Messages: []string{"feat(app): added embedded commit\n\n- Opened the commit UI."},
	}})
	m = updated.(model)
	if m.commitRunning || m.status != "Committed · feat(app): added embedded commit" {
		t.Fatalf("commit completion = running %v, status %q", m.commitRunning, m.status)
	}

	updated, _ = m.Update(commitFinishedMsg{})
	m = updated.(model)
	if m.status != "Commit cancelled" {
		t.Fatalf("commit cancellation status = %q", m.status)
	}

	updated, _ = m.Update(commitFinishedMsg{err: errors.New("boom")})
	m = updated.(model)
	if m.status != "Commit failed · boom" {
		t.Fatalf("commit failure status = %q", m.status)
	}
}

func TestRuntimeInitializationRendersBeforeServicesAreReady(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "loading-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.screen = screenChat
	m.config = value
	m.initializing = true
	m.status = "Starting Gateway and workspace services…"
	m.startup = func() tea.Msg { return runtimeInitializedMsg{} }
	m.resize(96, 24)

	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "loading-model") ||
		!strings.Contains(view, "Starting Gateway and workspace services") ||
		!strings.Contains(view, "starting workspace services") {
		t.Fatalf("startup view is not immediately useful:\n%s", view)
	}
	if m.Init() == nil {
		t.Fatal("startup command was not scheduled from Init")
	}

	configuredClient := &fakeClient{}
	updated, _ := m.Update(runtimeInitializedMsg{config: value, client: configuredClient})
	m = updated.(model)
	if m.initializing || m.screen != screenChat || m.client != configuredClient {
		t.Fatalf("initialized state = initializing %v, screen %v, client %#v", m.initializing, m.screen, m.client)
	}
}

func TestHelpScrollsAndRestoresDirtyIgnoreEditor(t *testing.T) {
	root := t.TempDir()
	workspaceStore := workspace.Store{Root: root}
	if err := workspaceStore.SaveIgnore("vendor/\n"); err != nil {
		t.Fatal(err)
	}
	value := config.Default()
	value.Provider.Model = "test-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspaceStore
	m.enterChat(value, &fakeClient{})
	updated, _ := m.enterIgnore()
	m = updated.(model)
	m.ignoreEditor.SetValue("vendor/\nbuild/\n")
	m.resize(80, 18)

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	m = updated.(model)
	if m.helpViewport.TotalLineCount() <= m.helpViewport.VisibleLineCount() {
		t.Fatalf("help is not scrollable: total %d visible %d", m.helpViewport.TotalLineCount(), m.helpViewport.VisibleLineCount())
	}
	if viewHeight := lipgloss.Height(ansi.Strip(m.View().Content)); viewHeight > m.height {
		t.Fatalf("help view height %d exceeds terminal %d", viewHeight, m.height)
	}
	before := m.helpViewport.ScrollPercent()
	updated, _ = m.updateHelp(tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = updated.(model)
	if m.helpViewport.ScrollPercent() <= before {
		t.Fatalf("help did not scroll: before %.2f after %.2f", before, m.helpViewport.ScrollPercent())
	}
	updated, _ = m.updateHelp(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(model)
	if m.screen != screenIgnore || m.ignoreEditor.Value() != "vendor/\nbuild/\n" {
		t.Fatalf("help did not restore ignore editor: screen %v content %q", m.screen, m.ignoreEditor.Value())
	}
}

func TestHelpKeepsReceivingSubagentEvents(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, &fakeClient{})
	m.waiting = true
	m.resize(90, 22)
	updated, _ := m.enterHelp()
	m = updated.(model)

	activity := agentActivity{Agent: "scout", Action: "thinking", Detail: "inspecting workspace"}
	updated, _ = m.Update(agentEventMsg{event: agentEvent{activity: &activity}})
	m = updated.(model)
	if m.screen != screenHelp || len(m.agentActivities) != 1 || m.agentActivities[0].Agent != "scout" {
		t.Fatalf("background event state = screen %v activities %#v", m.screen, m.agentActivities)
	}

	updated, _ = m.updateHelp(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(model)
	if m.screen != screenChat || !m.waiting {
		t.Fatalf("return state = screen %v waiting %v", m.screen, m.waiting)
	}
}

func TestManagedSlashGatewayOpensGatewaySettings(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "local/test-model"
	value.UseManagedGateway()
	runtime := &fakeProviderRuntime{
		endpoint: "http://127.0.0.1:54321/v1",
		config: gateway.Config{Providers: []gateway.ProviderConfig{{
			ID: "local", Type: "openai-compatible", Enabled: true, BaseURL: "http://localhost:1234/v1",
		}}},
	}
	fake := &fakeClient{}
	m := newManagedModel(context.Background(), config.Store{Dir: t.TempDir()}, nil, runtime)
	m.enterChat(value, fake)
	m.input.SetValue("/gateway")

	updated, _ := m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(deferredSubmitMsg{})
	m = updated.(model)
	if m.screen != screenGateway || m.input.Value() != "" || len(m.gatewayConfig.Providers) != 1 {
		t.Fatalf("Gateway command state = screen %v, input %q, providers %#v", m.screen, m.input.Value(), m.gatewayConfig.Providers)
	}
	m.gatewayCursor = gatewaySectionProviders
	updated, _ = m.updateGateway(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.screen != screenProviders || m.providerReturn != screenGateway {
		t.Fatalf("provider section state = screen %v, return %v", m.screen, m.providerReturn)
	}
	updated, _ = m.updateProviders(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(model)
	if m.screen != screenGateway {
		t.Fatalf("provider escape screen = %v", m.screen)
	}
}

func TestGatewaySettingsSaveNetworkAndManageAPIKeys(t *testing.T) {
	directory := t.TempDir()
	store := config.Store{Dir: directory}
	runtime := &fakeProviderRuntime{endpoint: "http://127.0.0.1:54321/v1"}
	m := newManagedModel(context.Background(), store, nil, runtime)
	m.enterGatewaySettings()

	updated, _ := m.updateGateway(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.screen != screenGatewayNetwork {
		t.Fatalf("network screen = %v", m.screen)
	}
	m.gatewayHostInput.SetValue("0.0.0.0")
	m.gatewayPortInput.SetValue("18181")
	updated, command := m.updateGatewayNetwork(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = updated.(model)
	if command == nil {
		t.Fatal("network save command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if m.gatewaySettings.Server.Host != "0.0.0.0" || m.gatewaySettings.Server.Port != 18181 {
		t.Fatalf("saved network = %#v", m.gatewaySettings.Server)
	}

	m.enterGatewaySettings()
	m.gatewayCursor = gatewaySectionKeys
	updated, _ = m.updateGateway(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.updateGatewayKeys(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = updated.(model)
	m.gatewayKeyAlias.SetValue("desktop")
	updated, command = m.updateGatewayKeys(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if command == nil {
		t.Fatal("API key generation command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	secret := m.generatedGatewayKey
	if !strings.HasPrefix(secret, "qk_") || len(m.gatewaySettings.APIKeys) != 1 {
		t.Fatalf("generated key state = secret prefix %v, records %#v", strings.HasPrefix(secret, "qk_"), m.gatewaySettings.APIKeys)
	}
	settingsBody, err := os.ReadFile(gatewayconfig.Store{Dir: directory}.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(settingsBody), secret) {
		t.Fatal("plaintext API key was persisted")
	}
	masterKey, err := (gatewayconfig.Store{Dir: directory}).LoadMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	if !gatewayconfig.VerifyAPIKey(masterKey, m.gatewaySettings.APIKeys[0], secret) {
		t.Fatal("generated API key does not verify")
	}

	updated, _ = m.updateGatewayKeys(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.updateGatewayKeys(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = updated.(model)
	if !m.gatewayKeyRevokeArmed {
		t.Fatal("first revoke keypress did not arm confirmation")
	}
	updated, command = m.updateGatewayKeys(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = updated.(model)
	if command == nil {
		t.Fatal("API key revoke command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if m.gatewaySettings.ActiveKeyCount() != 0 || m.gatewaySettings.APIKeys[0].RevokedAt == nil {
		t.Fatalf("revoked keys = %#v", m.gatewaySettings.APIKeys)
	}
}

func TestGatewayArrowNavigationFocusesEditablePort(t *testing.T) {
	m := newManagedModel(context.Background(), config.Store{Dir: t.TempDir()}, nil, &fakeProviderRuntime{})
	m.enterGatewaySettings()
	m.enterGatewayNetwork()
	m.gatewayPortInput.SetValue("")
	updated, _ := m.updateGatewayNetwork(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(model)
	if m.gatewayNetworkFocus != 1 || !m.gatewayPortInput.Focused() || m.gatewayHostInput.Focused() {
		t.Fatalf("Gateway input focus = index %d, host %v, port %v", m.gatewayNetworkFocus, m.gatewayHostInput.Focused(), m.gatewayPortInput.Focused())
	}
	updated, _ = m.updateGatewayNetwork(tea.KeyPressMsg{Code: '1', Text: "1"})
	m = updated.(model)
	if m.gatewayPortInput.Value() != "1" {
		t.Fatalf("Gateway port input = %q", m.gatewayPortInput.Value())
	}
}

func TestManagedProviderAddAppliesWithoutRediscoveringModels(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "local/test-model"
	value.UseManagedGateway()
	runtime := &fakeProviderRuntime{
		endpoint: "http://127.0.0.1:54321/v1",
		config: gateway.Config{Providers: []gateway.ProviderConfig{{
			ID: "local", Type: "openai-compatible", Enabled: true, BaseURL: "http://localhost:1234/v1",
		}}},
	}
	fake := &fakeClient{models: []client.Model{{ID: "local/test-model"}, {ID: "codex/gpt-test"}}}
	m := newManagedModel(context.Background(), config.Store{Dir: t.TempDir()}, func(config.Config) (chatClient, error) {
		return fake, nil
	}, runtime)
	m.enterChat(value, &fakeClient{})
	m.enterProviderList()
	updated, _ := m.updateProviders(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = updated.(model)
	m.setup[setupProviderID].SetValue("codex")
	m.setup[setupProviderPrefix].SetValue("code")
	m.setProviderType("codex")
	m.setup[setupBaseURL].SetValue("")
	m.setup[setupAPIKeyEnv].SetValue("")

	updated, command := m.applyProviderEdit()
	m = updated.(model)
	if command == nil {
		t.Fatal("provider apply command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if runtime.applies != 1 || len(runtime.config.Providers) != 2 ||
		runtime.config.Providers[1].Type != "codex" || runtime.config.Providers[1].Prefix != "code" {
		t.Fatalf("runtime applies = %d, config = %#v", runtime.applies, runtime.config)
	}
	if m.screen != screenProviders || fake.listCalls != 0 || !strings.Contains(m.status, "saved") {
		t.Fatalf("screen = %v, model list calls = %d, status = %q", m.screen, fake.listCalls, m.status)
	}
}
func TestGatewayContextOverrideAppliesAndRefreshesActiveCache(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "local/vendor/model"
	value.Provider.ContextWindow = 131072
	value.Context.Window = 64000
	value.UseManagedGateway()
	runtime := &fakeProviderRuntime{
		endpoint: "http://127.0.0.1:54321/v1",
		config: gateway.Config{Providers: []gateway.ProviderConfig{{
			ID: "local", Type: "openai-compatible", Enabled: true, BaseURL: "http://localhost:1234/v1",
		}}},
	}
	currentClient := &fakeClient{}
	refreshedClient := &fakeClient{models: []client.Model{{
		ID: "local/vendor/model", ContextLength: 200000,
	}}}
	m := newManagedModel(context.Background(), store, func(config.Config) (chatClient, error) {
		return refreshedClient, nil
	}, runtime)
	m.enterChat(value, currentClient)
	kept := client.Message{Role: client.RoleUser, Content: "keep this history"}
	m.messages = append(m.messages, kept)
	m.memory.Append(kept)
	m.conversationID = "stale-conversation"
	m.modelReturn = screenChat
	m.modelChooseTarget = false
	m.enterModelPicker(value, []client.Model{{
		ID: "local/vendor/model", ContextLength: 131072,
	}})

	updated, _ := m.updateModelPicker(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m = updated.(model)
	if m.modelPickerStage != modelPickerContextWindow || !strings.Contains(m.View().Content, "Gateway model context") {
		t.Fatalf("context editor stage = %v, view = %q", m.modelPickerStage, m.View().Content)
	}
	m.modelContextWindow.SetValue("200000")
	updated, command := m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if command == nil {
		t.Fatal("context metadata apply command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)

	metadata := runtime.config.Providers[0].ModelMetadata["vendor/model"]
	if runtime.applies != 1 || metadata.ContextLength != 200000 {
		t.Fatalf("runtime applies = %d, metadata = %#v", runtime.applies, metadata)
	}
	if m.config.Provider.ContextWindow != 200000 || m.config.EffectiveContextWindow() != 200000 ||
		m.memory.Stats().ContextWindow != 200000 {
		t.Fatalf("active context config = %#v, stats = %#v", m.config, m.memory.Stats())
	}
	if m.conversationID != "" || len(m.messages) != 2 ||
		m.messages[1].Role != kept.Role || m.messages[1].Content != kept.Content {
		t.Fatalf("conversation refresh lost state: id = %q, messages = %#v", m.conversationID, m.messages)
	}
	if !currentClient.closed || m.client != refreshedClient || !strings.Contains(m.View().Content, "Gateway override") {
		t.Fatalf("client refreshed = %v, active client = %#v, view = %q", currentClient.closed, m.client, m.View().Content)
	}
	loaded, err := store.Load()
	if err != nil || loaded.Provider.ContextWindow != 200000 {
		t.Fatalf("persisted config = %#v, err = %v", loaded, err)
	}
}

func TestRefreshModelContextWindowClearsStaleCache(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "local/test-model"
	value.Provider.ContextWindow = 262144
	value.Context.Window = 65536

	refreshed, found := refreshModelContextWindow(value, []client.Model{{
		ID: "local/test-model",
	}})
	if !found || refreshed.Provider.ContextWindow != 0 || refreshed.EffectiveContextWindow() != 65536 {
		t.Fatalf("refreshed config = %#v, found = %v", refreshed, found)
	}
}
func TestGatewayContextOverrideCanBeClearedWithoutDroppingOtherMetadata(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "local/test-model"
	value.Provider.ContextWindow = 200000
	value.Context.Window = 65536
	value.UseManagedGateway()
	runtime := &fakeProviderRuntime{
		endpoint: "http://127.0.0.1:54321/v1",
		config: gateway.Config{Providers: []gateway.ProviderConfig{{
			ID: "local", Type: "openai-compatible", Enabled: true,
			ModelMetadata: map[string]client.ModelMetadata{
				"test-model": {ContextLength: 200000, MaxOutputTokens: 8192},
			},
		}}},
	}
	refreshedClient := &fakeClient{models: []client.Model{{
		ID: "local/test-model", ContextLength: 131072, MaxOutputTokens: 8192,
	}}}
	m := newManagedModel(context.Background(), config.Store{Dir: t.TempDir()}, func(config.Config) (chatClient, error) {
		return refreshedClient, nil
	}, runtime)
	m.enterChat(value, &fakeClient{})
	m.modelReturn = screenChat
	m.enterModelPicker(value, []client.Model{{
		ID: "local/test-model", ContextLength: 200000, MaxOutputTokens: 8192,
	}})

	updated, _ := m.updateModelPicker(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m = updated.(model)
	m.modelContextWindow.SetValue("")
	updated, command := m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(command())
	m = updated.(model)

	metadata, found := runtime.config.Providers[0].ModelMetadata["test-model"]
	if !found || metadata.ContextLength != 0 || metadata.MaxOutputTokens != 8192 {
		t.Fatalf("remaining metadata = %#v, found = %v", metadata, found)
	}
	if m.config.Provider.ContextWindow != 0 || m.config.EffectiveContextWindow() != 65536 ||
		m.memory.Stats().ContextWindow != 65536 {
		t.Fatalf("refreshed context = %#v, stats = %#v", m.config.Provider, m.memory.Stats())
	}
}

func TestFirstManagedProviderIsAlwaysAppended(t *testing.T) {
	runtime := &fakeProviderRuntime{endpoint: "http://127.0.0.1:54321/v1"}
	fake := &fakeClient{models: []client.Model{{ID: "codex/gpt-test"}}}
	m := newManagedModel(context.Background(), config.Store{Dir: t.TempDir()}, func(config.Config) (chatClient, error) {
		return fake, nil
	}, runtime)
	if !m.providerAdding || m.providerEditIndex != -1 || len(m.gatewayConfig.Providers) != 0 {
		t.Fatalf("initial provider state = adding %v, edit index %d, providers %#v", m.providerAdding, m.providerEditIndex, m.gatewayConfig.Providers)
	}
	m.setup[setupProviderID].SetValue("codex")
	m.setProviderType("codex")
	m.setup[setupBaseURL].SetValue("")
	m.setup[setupAPIKeyEnv].SetValue("")

	updated, command := m.applyProviderEdit()
	m = updated.(model)
	if command == nil {
		t.Fatal("first provider apply command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if runtime.applies != 1 || len(runtime.config.Providers) != 1 || runtime.config.Providers[0].ID != "codex" {
		t.Fatalf("runtime applies = %d, config = %#v", runtime.applies, runtime.config)
	}
}

func TestGatewayConfigOnlySavesProviderWithoutOpeningChatOrModels(t *testing.T) {
	runtime := &fakeProviderRuntime{endpoint: "http://127.0.0.1:54321/v1"}
	factoryCalls := 0
	m := newManagedModel(context.Background(), config.Store{Dir: t.TempDir()}, func(config.Config) (chatClient, error) {
		factoryCalls++
		return &fakeClient{}, nil
	}, runtime)
	m.gatewayConfigOnly = true
	m.setup[setupProviderID].SetValue("codex")
	m.setProviderType("codex")
	m.setup[setupBaseURL].SetValue("")
	m.setup[setupAPIKeyEnv].SetValue("")

	updated, command := m.applyProviderEdit()
	m = updated.(model)
	if command == nil {
		t.Fatal("provider apply command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)

	if runtime.applies != 1 || len(m.gatewayConfig.Providers) != 1 {
		t.Fatalf("runtime applies = %d, Gateway config = %#v", runtime.applies, m.gatewayConfig)
	}
	if factoryCalls != 0 || m.client != nil || m.screen != screenProviders {
		t.Fatalf("factory calls = %d, client = %#v, screen = %v", factoryCalls, m.client, m.screen)
	}
	if !strings.Contains(m.status, "Provider settings saved") || !strings.Contains(m.viewProviders(), "esc quit") {
		t.Fatalf("status = %q, view = %q", m.status, m.viewProviders())
	}
	_, quit := m.updateProviders(tea.KeyPressMsg{Code: tea.KeyEscape})
	if quit == nil {
		t.Fatal("Esc did not quit Gateway config-only mode")
	}
}

func TestFirstManagedProviderIsSavedWhenModelDiscoveryFails(t *testing.T) {
	runtime := &fakeProviderRuntime{endpoint: "http://127.0.0.1:54321/v1"}
	fake := &fakeClient{listModelsErr: errors.New("provider is offline")}
	m := newManagedModel(context.Background(), config.Store{Dir: t.TempDir()}, func(config.Config) (chatClient, error) {
		return fake, nil
	}, runtime)
	m.setup[setupProviderID].SetValue("offline")
	m.setup[setupBaseURL].SetValue("http://127.0.0.1:1/v1")
	m.setup[setupAPIKeyEnv].SetValue("")

	updated, command := m.applyProviderEdit()
	m = updated.(model)
	if command == nil {
		t.Fatal("provider apply command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)

	if runtime.applies != 1 || len(runtime.config.Providers) != 1 ||
		len(m.gatewayConfig.Providers) != 1 || m.gatewayConfig.Providers[0].ID != "offline" {
		t.Fatalf("runtime applies = %d, runtime config = %#v, UI config = %#v", runtime.applies, runtime.config, m.gatewayConfig)
	}
	if m.screen != screenProviders || m.client != fake || fake.listCalls != 1 {
		t.Fatalf("screen = %v, client = %#v, model list calls = %d", m.screen, m.client, fake.listCalls)
	}
	if !strings.Contains(m.status, "Provider settings saved") || !strings.Contains(m.status, "model discovery unavailable") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestProviderTypeSelectorCyclesAndUpdatesUntouchedDefaults(t *testing.T) {
	runtime := &fakeProviderRuntime{}
	m := newManagedModel(context.Background(), config.Store{Dir: t.TempDir()}, nil, runtime)
	m.enterProviderEditor(-1)
	m.setupFocus = setupProviderType

	updated, _ := m.updateSetup(tea.KeyPressMsg{Code: tea.KeyRight})
	m = updated.(model)
	if selected := m.selectedProviderType(); selected.value != "openrouter" {
		t.Fatalf("selected type = %#v", selected)
	}
	if m.setup[setupBaseURL].Value() != "" || m.setup[setupAPIKeyEnv].Value() != "OPENROUTER_API_KEY" {
		t.Fatalf("defaults = base URL %q, API key env %q", m.setup[setupBaseURL].Value(), m.setup[setupAPIKeyEnv].Value())
	}
	if !strings.Contains(m.setup[setupBaseURL].Placeholder, "openrouter.ai") {
		t.Fatalf("base URL placeholder = %q", m.setup[setupBaseURL].Placeholder)
	}
	if !strings.Contains(m.viewSetup(), "OpenRouter") || !strings.Contains(m.viewSetup(), "select API type") {
		t.Fatalf("selector view = %q", m.viewSetup())
	}
}

func TestProviderKindSelectorPersistsGenericCompatibleProvider(t *testing.T) {
	runtime := &fakeProviderRuntime{endpoint: "http://127.0.0.1:54321/v1"}
	m := newManagedModel(context.Background(), config.Store{Dir: t.TempDir()}, nil, runtime)
	m.gatewayConfigOnly = true
	m.enterProviderEditor(-1)
	m.setup[setupProviderID].SetValue("strict-compatible")
	m.setup[setupBaseURL].SetValue("https://gateway.example/v1")
	m.setup[setupAPIKeyEnv].SetValue("")
	m.setupFocus = setupProviderKind

	updated, _ := m.updateSetup(tea.KeyPressMsg{Code: tea.KeyRight})
	m = updated.(model)
	if selected := m.selectedProviderKind(); selected.value != "generic" {
		t.Fatalf("selected kind = %#v", selected)
	}
	view := m.viewSetup()
	if !strings.Contains(view, "Generic") || !strings.Contains(view, "strict or unknown servers") ||
		!strings.Contains(view, "select provider kind") {
		t.Fatalf("kind selector view = %q", view)
	}

	updated, command := m.applyProviderEdit()
	m = updated.(model)
	if command == nil {
		t.Fatal("provider apply command is nil")
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if runtime.applies != 1 || len(runtime.config.Providers) != 1 ||
		runtime.config.Providers[0].Kind != "generic" {
		t.Fatalf("runtime applies = %d, config = %#v", runtime.applies, runtime.config)
	}
	if !strings.Contains(m.viewProviders(), "kind generic") {
		t.Fatalf("provider list omitted kind: %q", m.viewProviders())
	}
}

func TestProviderTypeSelectorClearsIncompatibleKindForNativeTransport(t *testing.T) {
	runtime := &fakeProviderRuntime{config: gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "custom", Type: "openai-compatible", Kind: "generic", Enabled: true,
		BaseURL: "https://gateway.example/v1",
	}}}}
	m := newManagedModel(context.Background(), config.Store{Dir: t.TempDir()}, nil, runtime)
	m.enterProviderEditor(0)
	if selected := m.selectedProviderKind(); selected.value != "generic" {
		t.Fatalf("loaded kind = %#v", selected)
	}
	m.setupFocus = setupProviderType

	updated, _ := m.updateSetup(tea.KeyPressMsg{Code: tea.KeyRight})
	m = updated.(model)
	if selectedType := m.selectedProviderType(); selectedType.value != "openrouter" {
		t.Fatalf("selected type = %#v", selectedType)
	}
	if selectedKind := m.selectedProviderKind(); selectedKind.value != "" {
		t.Fatalf("incompatible kind survived type change = %#v", selectedKind)
	}
	m.setupFocus = setupProviderKind
	if view := m.viewSetup(); !strings.Contains(view, "Native APIs accept only their") ||
		!strings.Contains(view, "matching kind; Auto is") {
		t.Fatalf("native kind constraint missing: %q", view)
	}
	updated, _ = m.updateSetup(tea.KeyPressMsg{Code: tea.KeyRight})
	m = updated.(model)
	if selectedKind := m.selectedProviderKind(); selectedKind.value != "openrouter" {
		t.Fatalf("explicit native kind = %#v", selectedKind)
	}

	m.setupFocus = setupProviderType
	updated, _ = m.updateSetup(tea.KeyPressMsg{Code: tea.KeyRight})
	m = updated.(model)
	if selectedType := m.selectedProviderType(); selectedType.value != "xai" {
		t.Fatalf("selected type = %#v", selectedType)
	}
	if selectedKind := m.selectedProviderKind(); selectedKind.value != "" {
		t.Fatalf("OpenRouter kind survived xAI type change = %#v", selectedKind)
	}
}

func TestProviderEditorRejectsDuplicateEffectivePrefixBeforeApply(t *testing.T) {
	runtime := &fakeProviderRuntime{config: gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "first", Prefix: "shared", Type: "openai-compatible", Enabled: true,
	}}}}
	m := newManagedModel(context.Background(), config.Store{Dir: t.TempDir()}, nil, runtime)
	m.enterProviderEditor(-1)
	m.setup[setupProviderID].SetValue("second")
	m.setup[setupProviderPrefix].SetValue("shared")

	updated, command := m.applyProviderEdit()
	m = updated.(model)
	if command != nil || runtime.applies != 0 || !strings.Contains(m.status, "already in use") {
		t.Fatalf("command = %v, applies = %d, status = %q", command != nil, runtime.applies, m.status)
	}
}

func TestSlashClearResetsConversation(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	fake := &fakeClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, fake)
	m.messages = append(m.messages,
		client.Message{Role: client.RoleUser, Content: "old question"},
		client.Message{Role: client.RoleAssistant, Content: "old answer"},
	)
	m.memory.Append(client.Message{Role: client.RoleUser, Content: "old question"})
	m.memory.Append(client.Message{Role: client.RoleAssistant, Content: "old answer"})
	m.conversationID = "conversation-1"
	m.input.SetValue("/clear")

	updated, _ := m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(deferredSubmitMsg{})
	m = updated.(model)
	if m.input.Value() != "" || m.conversationID != "" {
		t.Fatalf("input = %q, conversation ID = %q", m.input.Value(), m.conversationID)
	}
	if len(m.messages) != 1 || m.messages[0].Role != client.RoleSystem {
		t.Fatalf("transcript after clear = %#v", m.messages)
	}
	if got := m.memory.Messages(); len(got) != 1 || got[0].Role != client.RoleSystem {
		t.Fatalf("request context after clear = %#v", got)
	}
	if m.status != "Conversation cleared" {
		t.Fatalf("status = %q", m.status)
	}
}

func TestWorkspaceSessionPersistsAndRestoresWithGlobalChatConfig(t *testing.T) {
	workspaceStore := workspace.Store{Root: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "global-model"
	fake := &fakeClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspaceStore
	m.enterChat(value, fake)
	m = submitAndReceive(t, m, "remember this folder")

	session, err := workspaceStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Transcript) != 2 || session.Transcript[0].Content != "remember this folder" || len(session.Context) != 2 {
		t.Fatalf("saved workspace session = %#v", session)
	}
	if session.RunID == "" || session.RunID != m.runID {
		t.Fatalf("saved run ID = %q, model run ID = %q", session.RunID, m.runID)
	}
	m.conversationID = "cache_workspace_test"
	if err := m.saveWorkspaceSession(); err != nil {
		t.Fatal(err)
	}

	restoredConfig := value
	restoredConfig.Provider.SystemPrompt = "updated global prompt"
	restored := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	restored.workspaceStore = &workspaceStore
	restored.enterChat(restoredConfig, &fakeClient{})
	if restored.config.Provider.Model != "global-model" {
		t.Fatalf("workspace overrode global model: %q", restored.config.Provider.Model)
	}
	if restored.runID != session.RunID {
		t.Fatalf("restored run ID = %q, want %q", restored.runID, session.RunID)
	}
	if restored.conversationID != "" {
		t.Fatalf("restored process-local conversation ID = %q, want empty", restored.conversationID)
	}
	if len(restored.messages) != 3 || restored.messages[0].Content != "updated global prompt" ||
		restored.messages[1].Content != "remember this folder" {
		t.Fatalf("restored transcript = %#v", restored.messages)
	}
	requestContext := restored.memory.Messages()
	if len(requestContext) != 3 || requestContext[0].Content != "updated global prompt" {
		t.Fatalf("restored request context = %#v", requestContext)
	}
}

func TestChatHeaderShowsAbsoluteWorkspacePath(t *testing.T) {
	workspaceStore := workspace.Store{Root: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "global-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspaceStore
	m.enterChat(value, &fakeClient{})
	m.resize(120, 30)
	if view := m.View().Content; !strings.Contains(view, "workspace · "+workspaceStore.Root) {
		t.Fatalf("chat header does not show workspace path: %q", view)
	}
}

func TestClearRemovesWorkspaceSession(t *testing.T) {
	workspaceStore := workspace.Store{Root: t.TempDir()}
	if err := workspaceStore.Save(workspace.Session{
		Transcript: []client.Message{{Role: client.RoleUser, Content: "old"}},
		Context:    []client.Message{{Role: client.RoleUser, Content: "old"}},
	}); err != nil {
		t.Fatal(err)
	}
	value := config.Default()
	value.Provider.Model = "global-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspaceStore
	m.enterChat(value, &fakeClient{})
	m.resetConversation()
	if _, err := workspaceStore.Load(); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("workspace session survived clear: %v", err)
	}
}

func TestModelPickerFiltersModels(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	value := config.Default()
	value.Provider.Model = "temporary"
	m.enterModelPicker(value, []client.Model{{ID: "gpt-large"}, {ID: "embed-small"}, {ID: "gpt-mini"}})
	m.modelFilter.SetValue("gpt")
	filtered := m.filteredModels()
	if len(filtered) != 2 || filtered[0].ID != "gpt-large" || filtered[1].ID != "gpt-mini" {
		t.Fatalf("filtered models = %#v", filtered)
	}
	m.modelFilter.Reset()
	updated, _ := m.updateModelPicker(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = updated.(model)
	if m.modelPickerStage != modelPickerModels || m.modelFilter.Value() != "c" {
		t.Fatalf("plain c did not filter models: stage = %v, filter = %q", m.modelPickerStage, m.modelFilter.Value())
	}
}

func TestChatCarriesHistoryAcrossTurns(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	fake := &fakeClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, fake)

	m = submitAndReceive(t, m, "first")
	if len(fake.requests) != 1 || len(fake.requests[0].Messages) != 2 {
		t.Fatalf("first request = %#v", fake.requests)
	}
	m = submitAndReceive(t, m, "second")
	if len(fake.requests) != 2 || len(fake.requests[1].Messages) != 4 {
		t.Fatalf("second request messages = %#v", fake.requests[1].Messages)
	}
	if fake.requests[1].Messages[2].Role != client.RoleAssistant ||
		fake.requests[1].Messages[2].Content != "reply 1" ||
		fake.requests[1].ConversationID != "conversation-1" {
		t.Fatalf("second request = %#v", fake.requests[1])
	}
}

func TestChatExecutesToolCallsAndContinuesTurn(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "tool-model"
	workspaceStore := workspace.Store{Root: t.TempDir()}
	agentTools := &fakeAgentTools{}
	configuredClient := &toolCallingClient{}
	archiveStore, err := sessionstore.Open(workspaceStore.Root)
	if err != nil {
		t.Fatal(err)
	}
	archiveWriter := sessionstore.NewWriter(archiveStore, 16)
	t.Cleanup(func() { _ = archiveWriter.Close() })
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspaceStore
	m.toolRuntime = agentTools
	m.archive = archiveWriter
	m.enterChat(value, configuredClient)
	m.input.SetValue("create a Go project")
	updated, command := m.submitChat()
	m = updated.(model)

	updated, command = m.Update(nextAgentMessage(t, command))
	m = updated.(model)
	if !m.waiting || !strings.Contains(m.status, "Preparing tool") ||
		len(m.messages[len(m.messages)-1].ToolCalls) != 2 {
		t.Fatalf("tool-call progress = waiting %v, status %q, messages %#v", m.waiting, m.status, m.messages)
	}

	updated, command = m.Update(nextAgentMessage(t, command))
	m = updated.(model)
	if !strings.Contains(m.status, "Running tool") || !strings.Contains(m.status, "start task") {
		t.Fatalf("task-start progress status = %q", m.status)
	}

	updated, command = m.Update(nextAgentMessage(t, command))
	m = updated.(model)
	if m.messages[len(m.messages)-1].Name != taskStartToolName || !strings.Contains(m.status, "completed") {
		t.Fatalf("task-start result = status %q, messages %#v", m.status, m.messages)
	}

	updated, command = m.Update(nextAgentMessage(t, command))
	m = updated.(model)
	if !strings.Contains(m.status, "Running tool") || !strings.Contains(m.status, "write main.go") {
		t.Fatalf("tool-start progress status = %q", m.status)
	}

	updated, command = m.Update(nextAgentMessage(t, command))
	m = updated.(model)
	if m.messages[len(m.messages)-1].Role != client.RoleTool || !strings.Contains(m.status, "completed") {
		t.Fatalf("tool-result progress = status %q, messages %#v", m.status, m.messages)
	}

	for m.waiting {
		updated, command = m.Update(nextAgentMessage(t, command))
		m = updated.(model)
	}

	if len(configuredClient.requests) != 3 || len(configuredClient.requests[0].Tools) != 4 {
		t.Fatalf("agent requests = %#v", configuredClient.requests)
	}
	if configuredClient.requests[0].Tools[1].Function.Name != taskStartToolName ||
		configuredClient.requests[0].Tools[2].Function.Name != askToUserToolName ||
		configuredClient.requests[0].Tools[3].Function.Name != taskCompleteToolName {
		t.Fatalf("orchestration tools = %#v", configuredClient.requests[0].Tools)
	}
	runtimePromptFound := false
	for _, message := range configuredClient.requests[0].Messages {
		if strings.Contains(message.Content, "Runtime environment: OS=test-os; architecture=test-arch; run_command shell=test-shell.") &&
			strings.Contains(message.Content, "workspace-root .qignore") {
			runtimePromptFound = true
			break
		}
	}
	if !runtimePromptFound {
		t.Fatalf("first request omitted runtime environment or discovery policy: %#v", configuredClient.requests[0].Messages)
	}
	continuation := configuredClient.requests[1]
	if continuation.ConversationID != "tool-conversation" || len(continuation.Messages) < 2 {
		t.Fatalf("tool continuation = %#v", continuation)
	}
	completionRequest := configuredClient.requests[2]
	if len(completionRequest.Messages) < 2 || !strings.Contains(
		completionRequest.Messages[len(completionRequest.Messages)-1].Content, "started with task_start",
	) {
		t.Fatalf("completion enforcement request = %#v", completionRequest.Messages)
	}
	var assistantCall, toolResult client.Message
	for _, message := range continuation.Messages {
		if len(message.ToolCalls) == 2 {
			assistantCall = message
		}
		if message.ToolCallID == "call-1" {
			toolResult = message
		}
	}
	if len(assistantCall.ToolCalls) != 2 || toolResult.Role != client.RoleTool ||
		toolResult.ToolCallID != "call-1" || len(agentTools.calls) != 1 ||
		!strings.Contains(toolResult.Content, `"loom_ref":"loom://0123456789abcdef0123456789abcdef"`) {
		t.Fatalf("assistant call = %#v, tool result = %#v, calls = %#v", assistantCall, toolResult, agentTools.calls)
	}
	for requestIndex, request := range configuredClient.requests {
		for _, message := range request.Messages {
			if message.Role != client.RoleUser && message.Name != "" {
				t.Fatalf("request %d sent name %q on role %q", requestIndex, message.Name, message.Role)
			}
		}
	}
	if m.messages[len(m.messages)-1].Content != "Created main.go" || !strings.Contains(m.status, "Tools used · 1") {
		t.Fatalf("final transcript = %#v, status = %q", m.messages, m.status)
	}
	segment, learningMessages, ok := m.learning.Next()
	if !ok || segment.Reason != thinker.BoundaryTaskComplete || len(learningMessages) != 2 ||
		learningMessages[0].Role != client.RoleUser || learningMessages[1].Name != thinker.TaskCompleteEventName ||
		!strings.Contains(learningMessages[1].Content, `"summary":"Created main.go"`) {
		t.Fatalf("task completion learning segment = %#v, %#v", segment, learningMessages)
	}
	archived, err := archiveStore.Search(context.Background(), sessionstore.SearchOptions{
		Filters: sessionstore.Filters{RunIDs: []string{m.runID}},
		Sort:    sessionstore.SortOldest,
		Limit:   20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(archived.Hits) != 10 {
		t.Fatalf("archived records = %#v", archived.Hits)
	}
	var archivedToolCall, archivedToolResult, archivedCompletionCall, archivedCompletionResult *sessionstore.Record
	for index := range archived.Hits {
		record := &archived.Hits[index].Record
		if record.Kind == sessionstore.KindEvent && record.TaskID == "call-1" {
			archivedToolCall = record
		}
		if record.Kind == sessionstore.KindResult && record.TaskID == "call-1" {
			archivedToolResult = record
		}
		if record.Kind == sessionstore.KindEvent && record.TaskID == "complete-1" {
			archivedCompletionCall = record
		}
		if record.Kind == sessionstore.KindResult && record.TaskID == "complete-1" {
			archivedCompletionResult = record
		}
	}
	if archivedToolCall == nil || archivedToolCall.Status != sessionstore.StatusRunning || archivedToolCall.Summary != "write_file" {
		t.Fatalf("tool call record = %#v", archivedToolCall)
	}
	if archivedToolResult == nil || archivedToolResult.Status != sessionstore.StatusSucceeded || archivedToolResult.ParentID != archivedToolCall.ID {
		t.Fatalf("tool result record = %#v", archivedToolResult)
	}
	if archivedCompletionCall == nil || archivedCompletionResult == nil || archivedCompletionResult.ParentID != archivedCompletionCall.ID {
		t.Fatalf("completion records = call %#v, result %#v", archivedCompletionCall, archivedCompletionResult)
	}
}

func TestAskToUserPausesForAnswerAndResumesSameTask(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "tool-model"
	configuredClient := &askingClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.toolRuntime = &fakeAgentTools{}
	m.enterChat(value, configuredClient)
	m.resize(80, 24)
	m.input.SetValue("choose a color")
	updated, command := m.submitChat()
	m = updated.(model)

	for index := 0; index < 5; index++ {
		updated, command = m.Update(nextAgentMessage(t, command))
		m = updated.(model)
	}
	if !m.asking || !m.waiting || m.pendingQuestion.Question != "Which color?" ||
		!strings.Contains(m.View().Content, "› blue · Blue") {
		t.Fatalf("question state = asking %v, waiting %v, question %#v, view %q", m.asking, m.waiting, m.pendingQuestion, m.View().Content)
	}

	updated, _ = m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(model)
	if m.questionChoice != 1 || !strings.Contains(m.View().Content, "› green · Green") {
		t.Fatalf("selected choice = %d, view %q", m.questionChoice, m.View().Content)
	}
	updated, _ = m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(model)
	if m.questionChoice != 2 || !strings.Contains(m.View().Content, "› "+customAnswerLabel) {
		t.Fatalf("custom answer choice = %d, view %q", m.questionChoice, m.View().Content)
	}
	updated, command = m.submitQuestionAnswer("")
	m = updated.(model)
	if !m.asking || m.status != "Type a custom answer below" {
		t.Fatalf("empty custom answer submitted: asking %v status %q", m.asking, m.status)
	}
	updated, _ = m.updateChatKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = updated.(model)
	if m.questionChoice != 2 || m.input.Value() != "x" {
		t.Fatalf("custom answer input = choice %d value %q", m.questionChoice, m.input.Value())
	}
	m.input.Reset()
	updated, _ = m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(model)
	if m.questionChoice != 1 {
		t.Fatalf("choice after custom answer = %d", m.questionChoice)
	}
	updated, command = m.submitChat()
	m = updated.(model)
	if m.asking || !m.waiting || command == nil {
		t.Fatalf("answer submit state = asking %v, waiting %v, command %v", m.asking, m.waiting, command != nil)
	}
	for m.waiting {
		updated, command = m.Update(nextAgentMessage(t, command))
		m = updated.(model)
	}
	if len(configuredClient.requests) != 2 {
		t.Fatalf("requests = %#v", configuredClient.requests)
	}
	continuation := configuredClient.requests[1]
	if len(continuation.Messages) < 2 || !strings.Contains(continuation.Messages[len(continuation.Messages)-1].Content, `"selected_choice_id":"green"`) {
		t.Fatalf("question continuation = %#v", continuation.Messages)
	}
	if m.messages[len(m.messages)-1].Content != "Selection recorded" {
		t.Fatalf("final transcript = %#v", m.messages)
	}
}

func TestLongQuestionIsScrollableAndKeepsAnswerInputVisible(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.screen = screenChat
	m.waiting = true
	m.asking = true
	m.pendingQuestion = askToUserInput{
		Question: "Approve this plan?",
		// Keep each line just wider than the framed panel's content width. The
		// viewport and panel must agree on wrapping or the panel grows mid-scroll.
		Context: strings.Repeat(strings.Repeat("x", 91)+"\n", 48),
		Choices: []askToUserChoice{{ID: "approve", Label: "Approve"}},
	}
	m.input.Placeholder = "Answer the question…"
	m.input.Focus()
	m.resize(100, 30)

	if m.questionViewport.TotalLineCount() <= m.questionViewport.VisibleLineCount() {
		t.Fatalf("question did not become scrollable: total %d visible %d",
			m.questionViewport.TotalLineCount(), m.questionViewport.VisibleLineCount())
	}
	view := m.viewChat()
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "Answer the question…") || !strings.Contains(plain, "pgup/pgdn") {
		t.Fatalf("answer input or scroll help is not visible:\n%s", view)
	}
	if height := strings.Count(plain, "\n") + 1; height > m.height {
		t.Fatalf("chat view height %d exceeds terminal height %d", height, m.height)
	}
	inputRow := lineContaining(plain, "Answer the question…")
	questionHeight := lipgloss.Height(m.renderedQuestionViewport())
	terminalView := m.View()
	if terminalView.Cursor == nil || terminalView.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("question view cursor or mouse mode missing: cursor %v mouse %v", terminalView.Cursor != nil, terminalView.MouseMode)
	}

	before := m.questionViewport.ScrollPercent()
	for !m.questionViewport.AtBottom() {
		updated, _ := m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
		m = updated.(model)
		afterView := ansi.Strip(m.viewChat())
		if got := lipgloss.Height(m.renderedQuestionViewport()); got != questionHeight {
			t.Fatalf("question height changed while scrolling: before %d after %d", questionHeight, got)
		}
		if got := lineContaining(afterView, "Answer the question…"); got != inputRow {
			t.Fatalf("answer input moved while scrolling: before row %d after row %d", inputRow, got)
		}
		if height := strings.Count(afterView, "\n") + 1; height > m.height {
			t.Fatalf("scrolled chat view height %d exceeds terminal height %d", height, m.height)
		}
	}
	if m.questionViewport.ScrollPercent() <= before {
		t.Fatalf("question did not scroll: before %.2f after %.2f", before, m.questionViewport.ScrollPercent())
	}
}

func TestDetailedAgentTraceIsScrollableAndCollapsible(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.screen = screenChat
	m.waiting = true
	m.agentTraceExpanded = true
	m.agentStates = map[string]string{"coder": "running"}
	for index := 0; index < 12; index++ {
		m.appendAgentTrace(agentTrace{
			Agent: "coder", Kind: "tool_call", Name: "read_file",
			Content: fmt.Sprintf(`{"path":"app/file-%02d.go","detail":"%s"}`, index, strings.Repeat("evidence ", 20)),
		})
	}
	m.resize(100, 30)
	if m.agentTraceViewport.TotalLineCount() <= m.agentTraceViewport.VisibleLineCount() {
		t.Fatalf("agent trace did not become scrollable: total %d visible %d",
			m.agentTraceViewport.TotalLineCount(), m.agentTraceViewport.VisibleLineCount())
	}
	view := ansi.Strip(m.viewChat())
	if !strings.Contains(view, "SUBAGENT TRACE") || !strings.Contains(view, "coder · tool call · read_file") ||
		!strings.Contains(view, "ctrl+g collapse trace") {
		t.Fatalf("detailed trace view missing:\n%s", view)
	}
	before := m.agentTraceViewport.ScrollPercent()
	updated, _ := m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	m = updated.(model)
	if m.agentTraceViewport.ScrollPercent() >= before {
		t.Fatalf("agent trace did not scroll up: before %.2f after %.2f", before, m.agentTraceViewport.ScrollPercent())
	}
	updated, _ = m.updateChatKey(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	m = updated.(model)
	if m.agentTraceExpanded || strings.Contains(ansi.Strip(m.viewChat()), "SUBAGENT TRACE") {
		t.Fatalf("agent trace did not collapse: expanded %v", m.agentTraceExpanded)
	}
}

func TestAgentTraceNeverPushesQuestionInputBelowTerminal(t *testing.T) {
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.screen = screenChat
	m.waiting = true
	m.asking = true
	m.agentTraceExpanded = true
	m.agentStates = map[string]string{"griller": "waiting", "scout": "succeeded"}
	m.pendingQuestion = askToUserInput{
		Question: "Which benchmark scope should be used?",
		Context:  strings.Repeat("Long planning context. ", 20),
		Choices: []askToUserChoice{
			{ID: "current", Label: "Current scope", Description: strings.Repeat("Keep existing behavior. ", 8)},
			{ID: "expand", Label: "Expanded scope", Description: strings.Repeat("Add more coverage. ", 8)},
		},
	}
	m.input.Placeholder = "Type a custom answer…"
	for index := 0; index < 8; index++ {
		m.appendAgentTrace(agentTrace{
			Agent: "scout", Kind: "tool_result", Name: "read_file",
			Content: fmt.Sprintf(`{"content":%q,"path":"benchmark_test.go"}`, strings.Repeat("line content with wrapping ", 80)),
		})
	}
	m.resize(120, 36)

	view := ansi.Strip(m.viewChat())
	if height := lipgloss.Height(view); height > m.height {
		t.Fatalf("combined trace/question view height %d exceeds terminal %d:\n%s", height, m.height, view)
	}
	if !strings.Contains(view, "QUESTION") || !strings.Contains(view, "Type a custom answer…") {
		t.Fatalf("question input was pushed out of view:\n%s", view)
	}
	wantTraceHeight := m.agentTraceViewport.Height() + 2
	if got := lipgloss.Height(m.renderedAgentActivities()); got != wantTraceHeight {
		t.Fatalf("trace rendered height = %d; want viewport %d + chrome 2", got, m.agentTraceViewport.Height())
	}
}

func lineContaining(value, needle string) int {
	for index, line := range strings.Split(value, "\n") {
		if strings.Contains(line, needle) {
			return index
		}
	}
	return -1
}

func TestDirectAnswerDoesNotRequireTaskLifecycle(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "tool-model"
	configuredClient := &fakeClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.toolRuntime = &fakeAgentTools{}
	m.enterChat(value, configuredClient)
	m.input.SetValue("answer briefly")
	updated, command := m.submitChat()
	m = updated.(model)

	updated, _ = m.Update(nextAgentMessage(t, command))
	m = updated.(model)
	if m.waiting || len(configuredClient.requests) != 1 ||
		m.messages[len(m.messages)-1].Content != "reply 1" {
		t.Fatalf("direct answer state = waiting %v, requests %d, messages %#v", m.waiting, len(configuredClient.requests), m.messages)
	}
}

func TestTaskCompleteIsRejectedWithoutTaskStart(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "tool-model"
	configuredClient := &prematureCompletionClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.toolRuntime = &fakeAgentTools{}
	m.enterChat(value, configuredClient)
	m.input.SetValue("answer directly")
	updated, command := m.submitChat()
	m = updated.(model)

	for m.waiting {
		updated, command = m.Update(nextAgentMessage(t, command))
		m = updated.(model)
	}
	if len(configuredClient.requests) != 2 || m.messages[len(m.messages)-1].Content != "Direct answer after correction" {
		t.Fatalf("requests = %d, transcript = %#v", len(configuredClient.requests), m.messages)
	}
	foundError := false
	for _, message := range m.messages {
		if message.Name == taskCompleteToolName && strings.Contains(message.Content, "requires a successful task_start") {
			foundError = true
		}
	}
	if !foundError {
		t.Fatalf("premature task_complete error missing from transcript: %#v", m.messages)
	}
}

func TestCtrlCInterruptsActiveTurnAndRecordsCancellation(t *testing.T) {
	workspaceStore := workspace.Store{Root: t.TempDir()}
	archiveStore, err := sessionstore.Open(workspaceStore.Root)
	if err != nil {
		t.Fatal(err)
	}
	archiveWriter := sessionstore.NewWriter(archiveStore, 16)
	t.Cleanup(func() { _ = archiveWriter.Close() })
	value := config.Default()
	value.Provider.Model = "tool-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspaceStore
	m.archive = archiveWriter
	m.enterChat(value, &fakeClient{})
	m.beginTurn()
	oldTurnID := m.turnID
	turnContext := m.activeTurnContext()
	m.turnMessageStart = len(m.messages)
	userMessage := client.Message{Role: client.RoleUser, Content: "start a long task"}
	assistantCall := client.Message{
		Role: client.RoleAssistant,
		ToolCalls: []client.ToolCall{{
			ID: "pending-call", Type: client.ToolTypeFunction,
			Function: client.FunctionCall{Name: "run_command", Arguments: `{"command":"long-running"}`},
		}},
	}
	m.messages = append(m.messages, userMessage, assistantCall)
	m.memory.Append(userMessage)
	m.memory.Append(assistantCall)
	m.archiveMessage(userMessage, sessionstore.StatusSubmitted, false)
	m.archiveMessage(assistantCall, sessionstore.StatusSucceeded, false)
	m.pendingMessage = userMessage
	m.waiting = true

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = updated.(model)
	if m.waiting || m.turnID == oldTurnID || m.status != "Turn interrupted" {
		t.Fatalf("interrupted state = waiting %v, turn %d, status %q", m.waiting, m.turnID, m.status)
	}
	select {
	case <-turnContext.Done():
	default:
		t.Fatal("turn context was not cancelled")
	}
	last := m.messages[len(m.messages)-1]
	if last.Role != client.RoleTool || last.ToolCallID != "pending-call" || !strings.Contains(last.Content, "interrupted by user") {
		t.Fatalf("cancelled tool result = %#v", last)
	}
	session, err := workspaceStore.Load()
	if err != nil || len(session.Transcript) == 0 || session.Transcript[len(session.Transcript)-1].ToolCallID != "pending-call" {
		t.Fatalf("saved interrupted session = %#v, err = %v", session, err)
	}
	archived, err := archiveStore.Search(context.Background(), sessionstore.SearchOptions{
		Filters: sessionstore.Filters{RunIDs: []string{m.runID}}, Sort: sessionstore.SortOldest, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	foundTurnCancellation := false
	foundToolCancellation := false
	for _, hit := range archived.Hits {
		if hit.Record.Status == sessionstore.StatusCancelled && hit.Record.Summary == "turn interrupted" {
			foundTurnCancellation = true
		}
		if hit.Record.Status == sessionstore.StatusCancelled && hit.Record.TaskID == "pending-call" {
			foundToolCancellation = true
		}
	}
	if !foundTurnCancellation || !foundToolCancellation {
		t.Fatalf("cancellation records = %#v", archived.Hits)
	}
	messageCount := len(m.messages)
	updated, _ = m.Update(agentEventMsg{
		turnID: oldTurnID,
		event: agentEvent{response: &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{
			Role: client.RoleAssistant, Content: "late response",
		}}}}},
	})
	m = updated.(model)
	if len(m.messages) != messageCount {
		t.Fatalf("stale event changed transcript: %#v", m.messages)
	}
}

func TestTranscriptRendersToolActivityAsReadableProgress(t *testing.T) {
	transcript := renderTranscript([]client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
			ID: "call-1", Type: client.ToolTypeFunction,
			Function: client.FunctionCall{Name: "run_command", Arguments: `{"command":"go test ./..."}`},
		}}},
		{Role: client.RoleTool, Name: "wait", ToolCallID: "call-1",
			Content: `{"command_id":"cmd-1","status":"succeeded","exit_code":0,"output":"line one\nline two\n"}`},
	}, 100)
	for _, expected := range []string{"→ $ go test ./...", "✓ cmd-1", "exit 0", "line one", "line two"} {
		if !strings.Contains(transcript, expected) {
			t.Fatalf("rendered transcript does not contain %q: %q", expected, transcript)
		}
	}
	if strings.Contains(transcript, `\"command_id\"`) || strings.Contains(transcript, `line one\nline two`) {
		t.Fatalf("rendered transcript exposed raw JSON: %q", transcript)
	}
}
func TestTranscriptRendersInlineAndPreviewLoomResults(t *testing.T) {
	ref := "loom://0123456789abcdef0123456789abcdef"
	transcript := renderTranscript([]client.Message{
		{Role: client.RoleTool, Name: "wait", ToolCallID: "call-1",
			Content: `{"loom_ref":"` + ref + `","bytes":321,"stored":true,"result":{"command_id":"cmd-1","status":"succeeded","exit_code":0,"output":"tests passed\n"}}`},
		{Role: client.RoleTool, Name: "read_file", ToolCallID: "call-2",
			Content: `{"loom_ref":"` + ref + `","bytes":654,"stored":true,"result":{"path":"main.go","line_count":42,"total_lines":42}}`},
		{Role: client.RoleTool, Name: "search_archive", ToolCallID: "call-3",
			Content: `{"loom_ref":"` + ref + `","bytes":40000,"stored":true,"preview":"{\"hits\":[{\"id\":\"first\"}]}"}`},
	}, 100)
	plain := ansi.Strip(transcript)
	for _, expected := range []string{
		"✓ cmd-1", "exit 0", "tests passed", "✓ read main.go · 42/42 lines",
		"full result stored in Loom", `"id":"first"`, "Loom 0123456789ab…",
	} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("rendered Loom transcript does not contain %q: %q", expected, plain)
		}
	}
	if strings.Contains(plain, "•  ·") || strings.Contains(plain, `\"loom_ref\"`) {
		t.Fatalf("rendered Loom transcript exposed an empty result or raw receipt: %q", plain)
	}
}

func TestTranscriptRendersToolPanelAndFileDiffForBothThemes(t *testing.T) {
	message := client.Message{
		Role: client.RoleTool, Name: "edit_file", ToolCallID: "call-edit",
		Content: `{"path":"app/model.go","applied":1,"diff":"Diff preview:\n- 42#AB:old value\n+ 42#CD:new value"}`,
	}
	dark := renderTranscriptWithStyle([]client.Message{message}, 80, true)
	light := renderTranscriptWithStyle([]client.Message{message}, 80, false)
	for name, rendered := range map[string]string{"dark": dark, "light": light} {
		plain := ansi.Strip(rendered)
		for _, expected := range []string{
			"▌", "TOOL · edit_file", "✓ edited app/model.go · 1 change(s)",
			"+1 -1", "@@ change 1 @@", "- 42 │ old value", "+ 42 │ new value",
		} {
			if !strings.Contains(plain, expected) {
				t.Fatalf("%s tool diff does not contain %q: %q", name, expected, plain)
			}
		}
		if strings.Contains(plain, "#AB:") || strings.Contains(plain, "#CD:") {
			t.Fatalf("%s tool diff exposed hash anchors: %q", name, plain)
		}
		if !strings.Contains(rendered, "\x1b[48;5;") {
			t.Fatalf("%s tool panel has no background color: %q", name, rendered)
		}
	}
	if dark == light {
		t.Fatal("tool panel did not adapt to terminal theme")
	}
}

func TestTranscriptRendersAssistantMarkdownOnly(t *testing.T) {
	transcript := renderTranscriptWithStyle([]client.Message{
		{Role: client.RoleUser, Content: "**literal user input**"},
		{Role: client.RoleAssistant, Content: "# Result\n\n- **first** item\n- second\n\n```go\nfmt.Println(\"ok\")\n```"},
	}, 60, true)
	plain := ansi.Strip(transcript)
	for _, expected := range []string{"**literal user input**", "Result", "first item", "second", `fmt.Println("ok")`} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("rendered Markdown does not contain %q: %q", expected, plain)
		}
	}
	for _, rawMarkdown := range []string{"# Result", "```go", "**first**"} {
		if strings.Contains(plain, rawMarkdown) {
			t.Fatalf("assistant Markdown marker %q was not rendered: %q", rawMarkdown, plain)
		}
	}
}
func TestTranscriptCodeBlockUsesHighContrastSurfaceForBothThemes(t *testing.T) {
	message := client.Message{
		Role:    client.RoleAssistant,
		Content: "```go\nlogger := zerolog.New(os.Stdout).With().Timestamp().Logger()\n```",
	}
	for name, rendered := range map[string]string{
		"dark":  renderTranscriptWithStyle([]client.Message{message}, 100, true),
		"light": renderTranscriptWithStyle([]client.Message{message}, 100, false),
	} {
		plain := ansi.Strip(rendered)
		if !strings.Contains(plain, "logger := zerolog.New") {
			t.Fatalf("%s code block content missing: %q", name, plain)
		}
		hasHighContrastName := strings.Contains(rendered, "\x1b[38;5;117mlogger") ||
			strings.Contains(rendered, "139;233;253mlogger") ||
			strings.Contains(rendered, "139:233:253mlogger")
		if !hasHighContrastName {
			index := strings.Index(rendered, "logger")
			start := max(0, index-40)
			end := min(len(rendered), index+60)
			t.Fatalf("%s code block did not highlight its main text brightly: %q", name, rendered[start:end])
		}
		if strings.Contains(rendered, "\x1b[38;2;42;42;42mlogger") {
			t.Fatalf("%s code block used the low-contrast light-theme foreground: %q", name, rendered)
		}
	}
}

func TestTranscriptMarkdownWrapsAndTracksTerminalTheme(t *testing.T) {
	messages := []client.Message{{
		Role:    client.RoleAssistant,
		Content: "This is a long Markdown paragraph that must wrap to the current viewport width without changing the stored message.",
	}}
	dark := renderTranscriptWithStyle(messages, 42, true)
	light := renderTranscriptWithStyle(messages, 42, false)
	if dark == light {
		t.Fatal("dark and light Markdown styles rendered identically")
	}
	for _, line := range strings.Split(dark, "\n") {
		if width := ansi.StringWidth(line); width > 42 {
			t.Fatalf("rendered line width = %d, want <= 42: %q", width, ansi.Strip(line))
		}
	}
	if messages[0].Content != "This is a long Markdown paragraph that must wrap to the current viewport width without changing the stored message." {
		t.Fatalf("rendering mutated stored message: %#v", messages[0])
	}
}

func nextAgentMessage(t *testing.T, command tea.Cmd) agentEventMsg {
	t.Helper()
	if command == nil {
		t.Fatal("agent command is nil")
	}
	message := command()
	if event, ok := message.(agentEventMsg); ok {
		return event
	}
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		t.Fatalf("agent command returned %T", message)
	}
	for _, child := range batch {
		if event, ok := child().(agentEventMsg); ok {
			return event
		}
	}
	t.Fatal("batch did not contain an agent event")
	return agentEventMsg{}
}

func TestProviderSettingsCanBeCancelled(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	fake := &fakeClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, fake)
	m.enterSetup(value)
	updated, _ := m.updateSetup(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(model)
	if m.screen != screenChat || m.client != fake {
		t.Fatalf("model did not return to chat: %#v", m.screen)
	}
}

func submitAndReceive(t *testing.T, m model, content string) model {
	t.Helper()
	m.input.SetValue(content)
	updated, command := m.submitChat()
	m = updated.(model)
	if !m.waiting || command == nil {
		t.Fatal("chat was not submitted")
	}
	batch, ok := command().(tea.BatchMsg)
	if !ok {
		t.Fatalf("command did not return a batch")
	}
	var result chatResultMsg
	found := false
	for _, child := range batch {
		if message, ok := child().(chatResultMsg); ok {
			result = message
			found = true
		}
	}
	if !found {
		t.Fatal("batch did not contain a chat result")
	}
	updated, _ = m.Update(result)
	return updated.(model)
}
