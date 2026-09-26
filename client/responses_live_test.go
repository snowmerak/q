package client_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/providerhost"
)

// Run with Q_RESPONSES_INTEGRATION_MODEL=provider/model to verify a configured
// personal Gateway provider. The test never prints or copies provider secrets.
func TestIntegrationResponsesGateway(t *testing.T) {
	model := os.Getenv("Q_RESPONSES_INTEGRATION_MODEL")
	if model == "" {
		t.Skip("set Q_RESPONSES_INTEGRATION_MODEL to run a real Responses request")
	}
	prefix, _, ok := strings.Cut(model, "/")
	if !ok {
		t.Fatal("Q_RESPONSES_INTEGRATION_MODEL must be provider/model")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	value, err := (providerhost.Store{Dir: filepath.Join(home, ".q")}).Load()
	if err != nil {
		t.Fatal(err)
	}
	selected := make([]gateway.ProviderConfig, 0, 1)
	for _, provider := range value.Providers {
		id := provider.Prefix
		if id == "" {
			id = provider.ID
		}
		if id == prefix && provider.Enabled {
			selected = append(selected, provider)
		}
	}
	if prefix == "openrouter" && len(selected) == 0 {
		selected = append(selected, gateway.ProviderConfig{
			ID: "openrouter", Type: "openrouter", Enabled: true,
			APIKeyEnv: "OPENROUTER_API_KEY", Models: []string{strings.TrimPrefix(model, "openrouter/")},
		})
	}
	if len(selected) != 1 {
		t.Fatalf("enabled Gateway provider %q was not found", prefix)
	}
	value.Providers = selected
	configuredGateway, err := gateway.NewContext(t.Context(), value)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configuredGateway.Close() })
	if strings.HasSuffix(model, "/") {
		models, err := configuredGateway.Models(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range models {
			t.Log(candidate.ID)
		}
		return
	}
	server := httptest.NewServer(configuredGateway.Handler())
	t.Cleanup(server.Close)
	configured, err := client.New(client.Config{
		BaseURL: server.URL + "/v1", DisableAPIKey: true,
		ModelAPIModes: map[string]string{model: "responses"}, ForwardUsageMetadata: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configured.Close() })
	first, err := configured.Chat(t.Context(), client.ChatRequest{
		Model: model, Messages: []client.Message{{Role: client.RoleUser, Content: "Reply with one short word."}},
	})
	if os.Getenv("Q_RESPONSES_EXPECT_UNSUPPORTED") == "1" {
		if err == nil || !strings.Contains(err.Error(), "does not support native Responses") {
			t.Fatalf("expected native Responses rejection for %s, got response %#v and error %v", model, first, err)
		}
		chatClient, chatErr := client.New(client.Config{BaseURL: server.URL + "/v1", DisableAPIKey: true})
		if chatErr != nil {
			t.Fatal(chatErr)
		}
		t.Cleanup(func() { _ = chatClient.Close() })
		chatReply, chatErr := chatClient.Chat(t.Context(), client.ChatRequest{
			Model: model, Messages: []client.Message{{Role: client.RoleUser, Content: "Reply with one short word."}},
		})
		if chatErr != nil || len(chatReply.Choices) == 0 || chatReply.Choices[0].Message.Content == "" {
			t.Fatalf("Chat control request for %s failed: response %#v, error %v", model, chatReply, chatErr)
		}
		t.Logf("Native Responses rejected as expected for %s; Chat Completions succeeded", model)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Choices) != 1 || first.Choices[0].Message.Content == "" {
		t.Fatalf("first Responses turn has no answer: %#v", first)
	}
	second, err := configured.Chat(t.Context(), client.ChatRequest{
		Model: model, ConversationID: first.ConversationID,
		Messages: []client.Message{{Role: client.RoleUser, Content: "Reply with one short word."}, first.Choices[0].Message, {Role: client.RoleUser, Content: "Reply with a different short word."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Choices) != 1 || second.Choices[0].Message.Content == "" || second.ConversationID != first.ConversationID {
		t.Fatalf("second Responses turn did not continue: %#v", second)
	}
	t.Logf("Responses succeeded for %s: two turns; input tokens %d/%d; cached tokens %d/%d", model,
		first.Usage.PromptTokens, second.Usage.PromptTokens,
		first.Usage.PromptDetails.CachedTokens, second.Usage.PromptDetails.CachedTokens)

	tool := client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
		Name: "lookup", Description: "Look up a value", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"},
		},
	}}
	toolUser := client.Message{Role: client.RoleUser, Content: "Call lookup for the value of two plus two."}
	toolReply, err := configured.Chat(t.Context(), client.ChatRequest{
		Model: model, Messages: []client.Message{toolUser}, Tools: []client.Tool{tool}, ToolChoice: client.NamedToolChoice("lookup"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(toolReply.Choices) != 1 || len(toolReply.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("Responses did not return a function call: %#v", toolReply)
	}
	call := toolReply.Choices[0].Message.ToolCalls[0]
	final, err := configured.Chat(t.Context(), client.ChatRequest{
		Model: model, ConversationID: toolReply.ConversationID, Tools: []client.Tool{tool}, ToolChoice: client.ToolChoiceNone,
		Messages: []client.Message{toolUser, toolReply.Choices[0].Message, {Role: client.RoleTool, ToolCallID: call.ID, Content: "4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Choices) != 1 || final.Choices[0].Message.Content == "" {
		t.Fatalf("Responses did not continue after the function result: %#v", final)
	}
	t.Logf("Responses function call succeeded for %s: %s; output items %d", model, call.Function.Name, len(toolReply.Choices[0].Message.ResponseOutput))

	stream, err := configured.ChatStream(t.Context(), client.ChatRequest{
		Model: model, Messages: []client.Message{{Role: client.RoleUser, Content: "Reply with one short word."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	var streamedText string
	var completed bool
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta != nil && choice.Phase != "commentary" {
				streamedText += choice.Delta.Content
			}
			if choice.FinishReason != "" && choice.Delta != nil && len(choice.Delta.ResponseOutput) > 0 {
				completed = true
			}
		}
	}
	if !completed || streamedText == "" {
		t.Fatalf("Responses stream did not complete with text: completed=%v, text=%q", completed, streamedText)
	}

	picture := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			picture.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var encodedImage bytes.Buffer
	if err := png.Encode(&encodedImage, picture); err != nil {
		t.Fatal(err)
	}
	imageURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(encodedImage.Bytes())
	imageReply, err := configured.Chat(t.Context(), client.ChatRequest{
		Model: model, Messages: []client.Message{{Role: client.RoleUser, ContentParts: []client.MessageContentPart{
			{"type": "text", "text": "Name the color in this image in one word."},
			{"type": "image_url", "image_url": map[string]any{"url": imageURL}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(imageReply.Choices) != 1 || imageReply.Choices[0].Message.Content == "" {
		t.Fatalf("Responses image request has no answer: %#v", imageReply)
	}
	t.Logf("Responses streaming and image input succeeded for %s", model)
	if os.Getenv("Q_RESPONSES_CACHE_PROBE") == "1" {
		repeats := 180
		if strings.HasPrefix(model, "openrouter/anthropic/") {
			repeats = 350
		}
		prefix := strings.Repeat("Use the context below to answer briefly. The context is stable across turns. ", repeats)
		if strings.HasPrefix(model, "openrouter/anthropic/") {
			prefix = fmt.Sprintf("Cache probe %d. ", time.Now().UnixNano()) + prefix
		}
		initial := []client.Message{{Role: client.RoleSystem, Content: prefix}, {Role: client.RoleUser, Content: "Say alpha."}}
		cachedFirst, err := configured.Chat(t.Context(), client.ChatRequest{Model: model, Messages: initial})
		if err != nil {
			t.Fatal(err)
		}
		continued := append(append([]client.Message(nil), initial...), cachedFirst.Choices[0].Message, client.Message{Role: client.RoleUser, Content: "Say beta."})
		cachedSecond, err := configured.Chat(t.Context(), client.ChatRequest{Model: model, ConversationID: cachedFirst.ConversationID, Messages: continued})
		if err != nil {
			t.Fatal(err)
		}
		firstCached := cachedFirst.Usage.PromptDetails.CachedTokens
		secondCached := cachedSecond.Usage.PromptDetails.CachedTokens
		t.Logf("Responses cache probe for %s: input tokens %d/%d; cached tokens %d/%d; cache writes %d/%d", model,
			cachedFirst.Usage.PromptTokens, cachedSecond.Usage.PromptTokens, firstCached, secondCached,
			cachedFirst.Usage.PromptDetails.CacheWriteTokens, cachedSecond.Usage.PromptDetails.CacheWriteTokens)
		if secondCached == 0 {
			t.Fatal("Responses cache probe returned zero cached tokens for a stable long prefix")
		}
		if strings.HasPrefix(model, "openrouter/anthropic/") && cachedFirst.Usage.PromptDetails.CacheWriteTokens == 0 {
			t.Fatal("Claude Responses cache probe did not write the fresh prefix")
		}
	}
	if os.Getenv("Q_RESPONSES_SCHEMA_PROBE") == "1" {
		schema := map[string]any{
			"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}},
			"required": []string{"answer"}, "additionalProperties": false,
		}
		reply, err := configured.Chat(t.Context(), client.ChatRequest{
			Model: model, OutputSchema: schema,
			Messages: []client.Message{{Role: client.RoleUser, Content: "Return a JSON object with answer set to the word four."}},
		})
		if err != nil {
			t.Fatal(err)
		}
		var output struct {
			Answer string `json:"answer"`
		}
		if err := json.Unmarshal([]byte(reply.Choices[0].Message.Content), &output); err != nil || output.Answer != "four" {
			t.Fatalf("structured output for %s = %#v, error %v", model, reply.Choices[0].Message.Content, err)
		}
		t.Logf("Responses structured output succeeded for %s", model)
	}
}
