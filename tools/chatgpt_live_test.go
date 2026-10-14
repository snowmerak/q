package tools_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/llm-provider/providers/chatgpt"
	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/tools"
)

// Explicit opt-in: exercise native Responses and Chat-compatible caller modes through an existing Q login,
// without signing in, recording a session, or executing advertised tools.
func TestLiveChatGPTAcceptsQToolCatalog(t *testing.T) {
	if os.Getenv("Q_TEST_CHATGPT_TOOLS") != "1" {
		t.Skip("set Q_TEST_CHATGPT_TOOLS=1 to use the existing ChatGPT account")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	store, err := config.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(store.Dir, "chatgpt")
	if _, err := os.Stat(directory); os.IsNotExist(err) {
		t.Skip("Q has no local ChatGPT account store")
	} else if err != nil {
		t.Fatal(err)
	}
	p, err := chatgpt.New(chatgpt.Options{Directory: directory, AppID: "q", AppName: "Q"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	status, err := p.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	connected := false
	for _, profile := range status.Profiles {
		connected = connected || profile.ID == status.Active && profile.Connected && profile.PlanEnabled
	}
	if !connected {
		t.Skip("Q has no connected ChatGPT plan account; no login attempted")
	}
	models, err := p.ListModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model := ""
	for _, candidate := range models {
		if strings.Contains(candidate.ID, "gpt-6.1") && strings.Contains(candidate.ID, "sol") {
			model = candidate.ID
			break
		}
	}
	if model == "" {
		t.Skip("the requested GPT-6.1 Sol model is not available to the account")
	}
	runtime, err := tools.NewRuntime(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	catalog := append(runtime.Tools(), agentloop.OrchestrationTools()...)
	catalog = append(catalog, subagent.ExternalSearchTool(), subagent.ExternalWebTesterTool())
	catalog = append(catalog, subagent.DelegateTools()...)
	catalog = append(catalog, subagent.ChangeRequestTools()...)
	g, err := gateway.NewContext(ctx, providerhost.LocalConfig(gateway.Config{Providers: []gateway.ProviderConfig{{ID: "plan", Prefix: "plan", Type: "chatgpt", Enabled: true, Models: []string{model}}}}, store.Dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	server := httptest.NewServer(g.Handler())
	t.Cleanup(server.Close)
	configured, err := client.New(client.Config{BaseURL: server.URL + "/v1", DisableAPIKey: true, ForwardUsageMetadata: true, ModelAPIModes: map[string]string{"plan/" + model: "responses"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configured.Close() })
	chatCompatible, err := client.New(client.Config{BaseURL: server.URL + "/v1", DisableAPIKey: true, ForwardUsageMetadata: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = chatCompatible.Close() })
	request := client.ChatRequest{Model: "plan/" + model, Messages: []client.Message{{Role: client.RoleUser, Content: "Reply exactly OK. Do not call any tools."}}, Tools: catalog, ToolChoice: client.ToolChoiceAuto}
	for _, mode := range []string{"non-streaming", "streaming", "chat-completions-streaming"} {
		t.Run(mode, func(t *testing.T) {
			var response *client.ChatResponse
			var err error
			switch mode {
			case "chat-completions-streaming":
				response, _, err = agentloop.ConsumeChatStream(ctx, chatCompatible, request, nil)
			case "streaming":
				response, _, err = agentloop.ConsumeChatStream(ctx, configured, request, nil)
			default:
				response, err = configured.Chat(ctx, request)
			}
			if err != nil {
				t.Fatal(err)
			}
			if response == nil || len(response.Choices) == 0 {
				t.Fatal("missing assistant response")
			}
			message := response.Choices[0].Message
			if len(message.ToolCalls) > 0 || strings.TrimSpace(message.Content) != "OK" {
				t.Fatal("expected an OK answer without tool execution")
			}
			t.Logf("%s completed with %d Q tools advertised", model, len(catalog))
		})
	}
	if os.Getenv("Q_TEST_CHATGPT_CACHE") != "1" {
		return
	}
	for _, test := range []struct {
		name   string
		client *client.Client
	}{{"responses-cache", configured}, {"chat-completions-cache", chatCompatible}} {
		t.Run(test.name, func(t *testing.T) {
			prefix := "Cache probe " + time.Now().UTC().Format(time.RFC3339Nano) + ".\n" + strings.Repeat("Reference context: The cedar tree is green, the stone bridge crosses the river, and the library keeps maps. ", 200)
			request := client.ChatRequest{Model: "plan/" + model, Messages: []client.Message{{Role: client.RoleDeveloper, Content: prefix}, {Role: client.RoleUser, Content: "Reply exactly OK."}}}
			hit := false
			for round := 1; round <= 3; round++ {
				response, _, err := agentloop.ConsumeChatStream(ctx, test.client, request, nil)
				if err != nil {
					t.Fatal(err)
				}
				if response == nil || len(response.Choices) == 0 || response.Usage.PromptDetails == nil {
					t.Fatal("missing completed answer or cache usage")
				}
				if !strings.HasPrefix(response.ConversationID, "cache_") || request.ConversationID != "" && response.ConversationID != request.ConversationID {
					t.Fatal("cache affinity changed within the session")
				}
				cached := response.Usage.PromptDetails.CachedTokens
				hit = hit || round > 1 && cached > 0
				t.Logf("round %d: input=%d cached=%d", round, response.Usage.PromptTokens, cached)
				request.ConversationID = response.ConversationID
				request.Messages = append(request.Messages, response.Choices[0].Message, client.Message{Role: client.RoleUser, Content: "Reply exactly OK again."})
			}
			if !hit {
				t.Fatal("no cache hit in subsequent turns using automatic session affinity")
			}
		})
	}
}
