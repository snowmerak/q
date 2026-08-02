package app

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

type fakeClient struct {
	requests []client.ChatRequest
	models   []client.Model
	closed   bool
}

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
	updated, command := m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if !m.waiting || command == nil {
		t.Fatal("enter did not submit chat")
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
	if m.input.VirtualCursor() || m.View().Cursor == nil {
		t.Fatal("chat input is not using the real terminal cursor")
	}
	m = submitAndReceive(t, m, decomposed)
	if got := fake.requests[0].Messages[1].Content; got != "가" {
		t.Fatalf("submitted content = %q", got)
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

	updated, command := m.updateChatKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	if m.screen != screenModels || !m.discovering || command == nil {
		t.Fatalf("model discovery state = screen %v, discovering %v", m.screen, m.discovering)
	}
	updated, _ = m.Update(command())
	m = updated.(model)
	if m.screen != screenModels || len(m.models) != 2 {
		t.Fatalf("model picker state = screen %v, models %#v", m.screen, m.models)
	}
	m.modelCursor = 0 // new-model sorts before old-model.
	updated, command = m.updateModelPicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(model)
	updated, _ = m.Update(command())
	m = updated.(model)
	if m.screen != screenChat || m.config.Provider.Model != "new-model" {
		t.Fatalf("selected model = %q on screen %v", m.config.Provider.Model, m.screen)
	}
	if len(m.messages) != 2 || m.messages[1].Content != "keep me" {
		t.Fatalf("history was not preserved: %#v", m.messages)
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
	if m.screen != screenSetup || !m.setupEdit || m.input.Value() != "" {
		t.Fatalf("provider command state = screen %v, edit %v, input %q", m.screen, m.setupEdit, m.input.Value())
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
