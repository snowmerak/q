package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

type fakeProviderRuntime struct {
	endpoint string
	config   gateway.Config
	applies  int
}

func (f *fakeProviderRuntime) Endpoint() string       { return f.endpoint }
func (f *fakeProviderRuntime) Config() gateway.Config { return cloneGatewayConfig(f.config) }
func (f *fakeProviderRuntime) Apply(_ context.Context, value gateway.Config) error {
	f.config = cloneGatewayConfig(value)
	f.applies++
	return nil
}

type fakeClient struct {
	requests []client.ChatRequest
	models   []client.Model
	closed   bool
}

type fakeAgentTools struct {
	calls []client.ToolCall
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
	return client.ToolResult{Content: `{"path":"main.go"}`}, nil
}

type toolCallingClient struct {
	requests []client.ChatRequest
}

func (f *toolCallingClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	request.Messages = append([]client.Message(nil), request.Messages...)
	f.requests = append(f.requests, request)
	if len(f.requests) == 1 {
		return &client.ChatResponse{
			ConversationID: "tool-conversation",
			Choices: []client.Choice{{Message: client.Message{
				Role: client.RoleAssistant,
				ToolCalls: []client.ToolCall{{
					ID: "call-1", Type: client.ToolTypeFunction,
					Function: client.FunctionCall{Name: "write_file", Arguments: `{"path":"main.go","content":"package main"}`},
				}},
			}}},
		}, nil
	}
	return &client.ChatResponse{
		ConversationID: "tool-conversation",
		Choices: []client.Choice{{Message: client.Message{
			Role: client.RoleAssistant, Content: "Created main.go",
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
	view := m.View()
	if m.input.VirtualCursor() || view.Cursor == nil || view.Cursor.Shape != tea.CursorBar {
		t.Fatal("chat input is not using the real terminal cursor")
	}
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

func TestSlashModelConfiguresSubagentModelAndReasoning(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "default-model"
	fake := &fakeClient{models: []client.Model{
		{ID: "default-model"},
		{ID: "planning-model", Capabilities: &client.ModelCapabilities{Reasoning: &client.ReasoningCapabilities{
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
		if target == config.AgentRolePlanner {
			m.modelTargetCursor = index
			break
		}
	}
	updated, _ = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.modelPickerStage != modelPickerModels || m.modelTarget != config.AgentRolePlanner {
		t.Fatalf("planner target state = stage %v, target %q", m.modelPickerStage, m.modelTarget)
	}
	m.modelCursor = 1
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

	agent := m.config.Agents.Roles[config.AgentRolePlanner]
	if m.screen != screenModels || m.modelPickerStage != modelPickerTargets ||
		agent.Model != "planning-model" || agent.ReasoningEffort != "high" {
		t.Fatalf("saved planner config = %#v on screen %v", agent, m.screen)
	}
	if targets[m.modelTargetCursor] != config.AgentRolePlanner {
		t.Fatalf("target cursor moved to %q", targets[m.modelTargetCursor])
	}
	if fake.closed {
		t.Fatal("subagent model change replaced the main chat client")
	}
	if len(m.messages) != 2 || m.messages[1].Content != "keep me" {
		t.Fatalf("history was not preserved: %#v", m.messages)
	}
	loaded, err := store.Load()
	if err != nil || loaded.Agents.Roles[config.AgentRolePlanner] != agent {
		t.Fatalf("loaded config = %#v, err = %v", loaded, err)
	}
	updated, _ = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(model)
	if m.screen != screenChat {
		t.Fatalf("escape returned to screen %v", m.screen)
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

func TestSlashProviderOpensProviderSettings(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	fake := &fakeClient{}
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, fake)
	m.input.SetValue("/provider")

	updated, _ := m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(deferredSubmitMsg{})
	m = updated.(model)
	if m.screen != screenSetup || !m.setupEdit || m.input.Value() != "" {
		t.Fatalf("provider command state = screen %v, edit %v, input %q", m.screen, m.setupEdit, m.input.Value())
	}
}

func TestManagedSlashProviderOpensProviderList(t *testing.T) {
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
	m.input.SetValue("/provider")

	updated, _ := m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(deferredSubmitMsg{})
	m = updated.(model)
	if m.screen != screenProviders || m.input.Value() != "" || len(m.gatewayConfig.Providers) != 1 {
		t.Fatalf("provider command state = screen %v, input %q, providers %#v", m.screen, m.input.Value(), m.gatewayConfig.Providers)
	}
}

func TestManagedProviderAddAppliesThenOpensModelPicker(t *testing.T) {
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
	if m.screen != screenModels || len(m.models) != 2 {
		t.Fatalf("screen = %v, models = %#v, status = %q", m.screen, m.models, m.status)
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

	restoredConfig := value
	restoredConfig.Provider.SystemPrompt = "updated global prompt"
	restored := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	restored.workspaceStore = &workspaceStore
	restored.enterChat(restoredConfig, &fakeClient{})
	if restored.config.Provider.Model != "global-model" {
		t.Fatalf("workspace overrode global model: %q", restored.config.Provider.Model)
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
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.workspaceStore = &workspaceStore
	m.toolRuntime = agentTools
	m.enterChat(value, configuredClient)
	m.input.SetValue("create a Go project")
	updated, command := m.submitChat()
	m = updated.(model)

	updated, command = m.Update(nextAgentMessage(t, command))
	m = updated.(model)
	if !m.waiting || !strings.Contains(m.status, "Preparing tool") ||
		len(m.messages[len(m.messages)-1].ToolCalls) != 1 {
		t.Fatalf("tool-call progress = waiting %v, status %q, messages %#v", m.waiting, m.status, m.messages)
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

	updated, _ = m.Update(nextAgentMessage(t, command))
	m = updated.(model)

	if len(configuredClient.requests) != 2 || len(configuredClient.requests[0].Tools) != 1 {
		t.Fatalf("agent requests = %#v", configuredClient.requests)
	}
	continuation := configuredClient.requests[1]
	if continuation.ConversationID != "tool-conversation" || len(continuation.Messages) < 2 {
		t.Fatalf("tool continuation = %#v", continuation)
	}
	assistantCall := continuation.Messages[len(continuation.Messages)-2]
	toolResult := continuation.Messages[len(continuation.Messages)-1]
	if len(assistantCall.ToolCalls) != 1 || toolResult.Role != client.RoleTool ||
		toolResult.ToolCallID != "call-1" || len(agentTools.calls) != 1 {
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
