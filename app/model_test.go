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
	fake := &fakeClient{models: []client.Model{{ID: "z-model"}, {ID: "test-model"}}}
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
	if loaded.Provider.Model != "test-model" || loaded.Provider.APIKey != "super-secret" {
		t.Fatalf("loaded config = %#v", loaded)
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
