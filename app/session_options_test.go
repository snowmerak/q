package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
)

type retainedSessionTools struct{ calls int }

func (*retainedSessionTools) Tools() []client.Tool {
	return []client.Tool{{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "read_evidence", Parameters: map[string]any{"type": "object"}}}}
}
func (*retainedSessionTools) Environment() tools.HostEnvironment { return tools.HostEnvironment{} }
func (runtime *retainedSessionTools) Call(context.Context, client.ToolCall) (client.ToolResult, error) {
	runtime.calls++
	return client.ToolResult{Content: "durable evidence"}, nil
}

type retainedSessionClient struct {
	fakeClient
	fail  bool
	block chan struct{}
	empty bool
}

func (model *retainedSessionClient) Chat(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	request.Messages = append([]client.Message(nil), request.Messages...)
	model.requests = append(model.requests, request)
	if block := model.block; block != nil {
		close(block)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if model.empty {
		return &client.ChatResponse{ID: "response-123", Usage: client.Usage{PromptTokens: 321, CompletionTokens: 64}, Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant}, FinishReason: "length"}}}, nil
	}
	if model.fail {
		model.fail = false
		return nil, errors.New("request interrupted")
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role == client.RoleUser && last.Content == "opinion" {
		return &client.ChatResponse{ConversationID: "cache_session", Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "read-1", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "read_evidence", Arguments: "{}"}}}}}}}, nil
	}
	return &client.ChatResponse{ConversationID: "cache_session", Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, Content: "answer", ResponseModel: request.Model, ResponseOutput: []json.RawMessage{json.RawMessage(`{"type":"reasoning","id":"retained-reasoning"}`)}}}}}, nil
}

func TestSessionOptionsRetainActualQContextAndResumeAcrossHosts(t *testing.T) {
	settings := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "provider/member"
	value.ModelAPIModes = map[string]string{"provider/member": "responses"}
	value.Agents.MaxParallel = 2
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	store, lock, err := workspace.CreateSession(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(workspace.Session{}); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	configured := &retainedSessionClient{fakeClient: fakeClient{models: []client.Model{{ID: "provider/member", ContextLength: 1_000_000}}}}
	runtime := &retainedSessionTools{}
	opens := 0
	newHost := func() *SessionHost {
		manager, err := providerhost.NewManager(t.Context(), providerhost.Store{Dir: settings.Dir})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = manager.Close() })
		host := &SessionHost{ctx: t.Context(), store: settings, providerReady: true, manager: manager, factory: func(config.Config) (chatClient, error) { opens++; return configured, nil }}
		t.Cleanup(func() {
			if err := host.Close(); err != nil {
				t.Error(err)
			}
		})
		return host
	}
	options := SessionOptions{Model: "provider/member", ReasoningEffort: "high", SystemPrompt: "Stable council instructions", DisableLearning: true, RuntimeKey: "read-only", RuntimeFactory: func(context.Context, workspace.Store) (AgentToolRuntime, io.Closer, error) { return runtime, nil, nil }}
	run := func(host *SessionHost, id, prompt string) (string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		options.OperationID = id
		var result string
		err := host.RunWithOptions(ctx, store, store.SessionID, prompt, options, func(event SessionEvent) error {
			if event.Type == "result" {
				result = event.Content
			}
			return nil
		})
		return result, err
	}
	host := newHost()
	if result, err := run(host, "round-1", "opinion"); err != nil || result != "answer" {
		t.Fatalf("opinion = %q, %v", result, err)
	}
	if result, err := run(host, "round-2", "review"); err != nil || result != "answer" {
		t.Fatalf("review = %q, %v", result, err)
	}
	if opens != 1 || runtime.calls != 1 || len(configured.requests) != 3 {
		t.Fatalf("session was recreated: opens=%d reads=%d requests=%d", opens, runtime.calls, len(configured.requests))
	}
	opinion, review := configured.requests[1], configured.requests[2]
	if !reflect.DeepEqual(opinion.Messages, review.Messages[:len(opinion.Messages)]) || !reflect.DeepEqual(opinion.Tools, review.Tools) {
		t.Fatal("review changed the opinion request prefix")
	}
	if review.ConversationID != "cache_session" || review.ReasoningEffort != "high" {
		t.Fatalf("provider state lost: %+v", review)
	}
	for _, tool := range review.Tools {
		if strings.HasPrefix(tool.Function.Name, "delegate") || tool.Function.Name == "run_command" {
			t.Fatalf("unauthorized tool %s", tool.Function.Name)
		}
	}
	configured.fail = true
	if _, err := run(host, "round-3", "integrate"); err == nil {
		t.Fatal("expected interrupted request")
	}
	before := configured.requests[len(configured.requests)-1]
	if err := host.ReleaseSession(store, store.SessionID); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Operation == nil || saved.Operation.Completed || !saved.Operation.ContextReady || len(saved.ResponseReplay) == 0 || saved.ResponseAffinity == nil {
		t.Fatalf("missing durable state: %+v", saved)
	}
	host = newHost()
	if result, err := run(host, "round-3", "integrate"); err != nil || result != "answer" {
		t.Fatalf("resume = %q, %v", result, err)
	}
	after := configured.requests[len(configured.requests)-1]
	beforeJSON, _ := json.Marshal(before.Messages)
	afterJSON, _ := json.Marshal(after.Messages)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatalf("resume changed context:\nbefore=%s\nafter=%s", beforeJSON, afterJSON)
	}
	if after.ConversationID != "cache_session" || runtime.calls != 1 {
		t.Fatal("resume lost affinity or reran a completed tool")
	}
	count := len(configured.requests)
	if result, err := run(host, "round-3", "integrate"); err != nil || result != "answer" {
		t.Fatalf("completed retry = %q, %v", result, err)
	}
	if len(configured.requests) != count {
		t.Fatal("completed operation was charged again")
	}
	if _, err := run(host, "round-3", "changed prompt"); err == nil {
		t.Fatal("operation accepted a different prompt")
	}
	configured.block = make(chan struct{})
	cancelContext, cancelRequest := context.WithCancel(t.Context())
	options.OperationID = "round-4"
	finished := make(chan error, 1)
	go func() {
		finished <- host.RunWithOptions(cancelContext, store, store.SessionID, "cancel me", options, nil)
	}()
	select {
	case <-configured.block:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	cancelRequest()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not finish")
	}
	configured.block = nil
	if _, err := run(host, "round-4", "cancel me"); err != nil {
		t.Fatalf("cancelled resume = %v", err)
	}
	cancelledPromptCount := 0
	for _, message := range configured.requests[len(configured.requests)-1].Messages {
		if message.Role == client.RoleUser && message.Content == "cancel me" {
			cancelledPromptCount++
		}
	}
	if cancelledPromptCount != 1 {
		t.Fatalf("cancellation duplicated the user prompt %d times", cancelledPromptCount)
	}
	configured.empty = true
	if _, err := run(host, "empty", "empty chair"); err == nil {
		t.Fatal("expected empty response error")
	} else {
		for _, detail := range []string{"empty assistant response", "finish_reason=length", "response_id=response-123", "prompt_tokens=321", "completion_tokens=64"} {
			if !strings.Contains(err.Error(), detail) {
				t.Fatalf("missing %s from %v", detail, err)
			}
		}
	}

	// A compacted pending request need not retain the original large prompt.
	// Reopening must use its saved anchor instead of re-inserting that prompt.
	if err := host.ReleaseSession(store, store.SessionID); err != nil {
		t.Fatal(err)
	}
	saved, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	saved.Context[len(saved.Context)-1] = client.Message{Role: client.RoleDeveloper, Name: memory.RequestAnchorName, Content: "Pending chair request retained by compaction"}
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}
	configured.empty = false
	host = newHost()
	if _, err := run(host, "empty", "empty chair"); err != nil {
		t.Fatalf("compacted resume = %v", err)
	}
	var anchor bool
	for _, message := range configured.requests[len(configured.requests)-1].Messages {
		if message.Role == client.RoleUser && message.Content == "empty chair" {
			t.Fatal("compacted prompt was inserted again")
		}
		anchor = anchor || strings.Contains(message.Content, "Pending chair request retained by compaction")
	}
	if !anchor {
		t.Fatal("compacted request anchor was lost")
	}

}
