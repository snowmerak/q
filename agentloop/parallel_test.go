package agentloop_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
	qtools "github.com/snowmerak/q/tools"
)

type parallelTools struct {
	pairs       []chan struct{}
	memorySaved atomic.Bool
	answered    atomic.Bool
	calls       atomic.Int32
}

func (*parallelTools) Tools() []client.Tool {
	return []client.Tool{{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "echo", Parameters: map[string]any{"type": "object"}}}}
}
func (*parallelTools) Environment() qtools.HostEnvironment { return qtools.HostEnvironment{} }
func (r *parallelTools) Call(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
	var input struct{ Pair, Index int }
	if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
		return client.ToolResult{}, err
	}
	r.calls.Add(1)
	if input.Pair > 0 && !r.memorySaved.Load() {
		return client.ToolResult{}, fmt.Errorf("call %s overtook memory", call.ID)
	}
	if input.Pair > 1 && !r.answered.Load() {
		return client.ToolResult{}, fmt.Errorf("call %s overtook user input", call.ID)
	}
	if input.Index == 1 {
		close(r.pairs[input.Pair])
	} else {
		select {
		case <-r.pairs[input.Pair]:
		case <-ctx.Done():
			return client.ToolResult{}, ctx.Err()
		}
	}
	return client.ToolResult{Content: call.ID}, nil
}

func TestOrdinaryToolBatchesPreserveMemoryQuestionsAndResultOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runtime := &parallelTools{pairs: []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}}
	call := func(id, name, arguments string) client.ToolCall {
		return client.ToolCall{ID: id, Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: name, Arguments: arguments}}
	}
	calls := []client.ToolCall{
		call("a", "echo", `{"pair":0,"index":0}`), call("b", "echo", `{"pair":0,"index":1}`),
		call("memory", memory.RecordFactTool, `{"fact":"first pair finished"}`),
		call("c", "echo", `{"pair":1,"index":0}`), call("d", "echo", `{"pair":1,"index":1}`),
		call("question", "ask_to_user", `{"question":"Continue?","choices":[{"id":"yes","label":"Yes"}]}`),
		call("e", "echo", `{"pair":2,"index":0}`), call("f", "echo", `{"pair":2,"index":1}`),
	}
	configured := &scriptedClient{chat: func(request client.ChatRequest) *client.ChatResponse {
		if request.ParallelToolCalls == nil || !*request.ParallelToolCalls {
			t.Error("ordinary model requests do not allow multiple tool calls")
		}
		if runtime.calls.Load() == 0 {
			return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, ToolCalls: calls}}}}
		}
		var ids []string
		for _, message := range request.Messages {
			if message.Role == client.RoleTool {
				ids = append(ids, message.ToolCallID)
			}
		}
		if !reflect.DeepEqual(ids, []string{"a", "b", "memory", "c", "d", "question", "e", "f"}) {
			t.Errorf("provider result order = %v", ids)
		}
		return finalResponse("done")
	}}
	events := make(chan agentloop.Event)
	go agentloop.RunAgentLoop(ctx, agentloop.Request{Client: configured, Tools: runtime, Model: "test", Messages: []client.Message{{Role: client.RoleUser, Content: "Run pairs"}}}, events)
	var ids []string
	completed := false
	for event := range events {
		if err := event.Err(); err != nil {
			t.Error(err)
		}
		if message, ok := event.Message(); ok && message.Role == client.RoleTool {
			ids = append(ids, message.ToolCallID)
			if event.MessageIsToolError() {
				t.Errorf("tool %s failed: %s", message.ToolCallID, message.Content)
			}
			if message.Name == memory.RecordFactTool {
				runtime.memorySaved.Store(true)
			}
		}
		if _, answer, ok := event.Question(); ok {
			if runtime.calls.Load() != 4 {
				t.Errorf("question crossed batch boundary: calls=%d", runtime.calls.Load())
			}
			runtime.answered.Store(true)
			answer <- agentloop.AgentAnswer{SelectedChoiceID: "yes"}
		}
		if _, ok := event.Result(); ok {
			completed = true
		}
	}
	if ctx.Err() != nil || !completed || runtime.calls.Load() != 6 || !reflect.DeepEqual(ids, []string{"a", "b", "memory", "c", "d", "question", "e", "f"}) {
		t.Fatalf("completed=%v calls=%d results=%v err=%v", completed, runtime.calls.Load(), ids, ctx.Err())
	}
}
