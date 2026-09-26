package subagent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

type recoveryToolRuntime struct{ calls int }

func (r *recoveryToolRuntime) Tools() []client.Tool {
	return []client.Tool{{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "write_file", Parameters: map[string]any{"type": "object"}}}}
}
func (r *recoveryToolRuntime) Call(context.Context, client.ToolCall) (client.ToolResult, error) {
	r.calls++
	return client.ToolResult{Content: "wrote"}, nil
}

func TestGeneralRunnerDoesNotExecuteToolAfterCheckpointFailure(t *testing.T) {
	tool := &recoveryToolRuntime{}
	start := client.ToolCall{ID: "start", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: TaskStartToolName, Arguments: `{"objective":"do work"}`}}
	write := client.ToolCall{ID: "write", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "write_file", Arguments: `{"path":"out.txt"}`}}
	initial := []client.Message{{Role: client.RoleSystem, Content: "system"}, {Role: client.RoleUser, Content: "do work"}, {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{start}}, client.ToolResultMessage(start, client.ToolResult{Content: `{"started":true}`})}
	checkpointErr := errors.New("checkpoint unavailable")
	modelCalls := 0
	configured := contextChatFunc(func(context.Context, client.ChatRequest) (*client.ChatResponse, error) {
		modelCalls++
		return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{write}}}}}, nil
	})
	state := GeneralRunState{Transcript: initial, Context: initial, Round: 1, Started: true, Spec: SpecCheckpoint{Model: "model", Candidate: 0}}
	_, err := (GeneralRunner{Client: configured, Tools: tool, Spec: Spec{Role: config.AgentRoleScout, Model: "model", Candidates: []client.ModelCandidate{{Model: "model"}}}, Definition: AgentDefinition{Info: DelegateInfo{Name: "workspace/worker", Kind: AgentKindInner, Role: config.AgentRoleScout}, SystemPrompt: "Work.", Tools: []string{"write_file"}, StrictTools: true}, Resume: &state, Checkpoint: func(GeneralRunState) error { return checkpointErr }}).Run(t.Context(), "do work")
	if !errors.Is(err, checkpointErr) || modelCalls != 1 || tool.calls != 0 {
		t.Fatalf("err=%v modelCalls=%d toolCalls=%d", err, modelCalls, tool.calls)
	}
}

func TestGeneralRunnerAssignsStableIDsBeforeCheckpoint(t *testing.T) {
	responses := 0
	configured := contextChatFunc(func(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		if request.ToolChoice == client.ToolChoiceNone {
			return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, Content: "ack"}}}}, nil
		}
		responses++
		name, args := TaskStartToolName, `{"objective":"do"}`
		if responses == 2 {
			name, args = TaskCompleteToolName, `{"outcome":"succeeded","summary":"done"}`
		}
		return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: name, Arguments: args}}}}}}}, nil
	})
	var checkpoints []GeneralRunState
	result, err := (GeneralRunner{Client: configured, Spec: Spec{Role: config.AgentRoleScout, Model: "model", Candidates: []client.ModelCandidate{{Model: "model"}}}, Definition: AgentDefinition{Info: DelegateInfo{Name: "workspace/worker", Kind: AgentKindInner, Role: config.AgentRoleScout}, SystemPrompt: "Work."}, Checkpoint: func(state GeneralRunState) error { checkpoints = append(checkpoints, state); return nil }}).Run(t.Context(), "do")
	if err != nil || result.Outcome != "succeeded" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	found := false
	for _, state := range checkpoints {
		for _, message := range state.Transcript {
			if len(message.ToolCalls) > 0 && message.ToolCalls[0].Function.Name == TaskStartToolName {
				if message.ToolCalls[0].ID == "" {
					t.Fatal("empty ID was checkpointed")
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("no task start checkpoint")
	}
}

func TestGeneralRunnerPreservesStartedLifecycleAcrossCompaction(t *testing.T) {
	start := client.ToolCall{ID: "start", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: TaskStartToolName, Arguments: `{"objective":"do work"}`}}
	read := client.ToolCall{ID: "read", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "read_file", Arguments: `{"path":"large.txt"}`}}
	messages := []client.Message{
		{Role: client.RoleSystem, Content: "system"},
		{Role: client.RoleUser, Content: "do work"},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{start}},
		client.ToolResultMessage(start, client.ToolResult{Content: `{"started":true}`}),
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{read}},
		client.ToolResultMessage(read, client.ToolResult{Content: strings.Repeat("large result ", 6_000)}),
	}
	modelRequests := 0
	configured := contextChatFunc(func(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		if isContextCheckpointRequest(request) {
			return contextResponse(contextCheckpointJSON("continue the active task")), nil
		}
		if request.ToolChoice == client.ToolChoiceNone {
			return contextResponse("ack"), nil
		}
		modelRequests++
		var callKept, resultKept bool
		for _, message := range request.Messages {
			for _, call := range message.ToolCalls {
				callKept = callKept || call.Function.Name == TaskStartToolName && call.ID == start.ID
			}
			resultKept = resultKept || message.Role == client.RoleTool && message.Name == TaskStartToolName && message.ToolCallID == start.ID
		}
		if !callKept || !resultKept {
			t.Fatalf("compacted request lost task_start exchange: %#v", request.Messages)
		}
		complete := client.ToolCall{ID: "complete", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: TaskCompleteToolName, Arguments: `{"outcome":"succeeded","summary":"done"}`}}
		return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{complete}}}}}, nil
	})
	state := GeneralRunState{
		Transcript: messages, Context: messages, Round: 2, Started: true,
		Spec: SpecCheckpoint{Model: "model", Candidate: 0},
	}
	definition := AgentDefinition{
		Info:         DelegateInfo{Name: "workspace/worker", Kind: AgentKindInner, Role: config.AgentRoleScout},
		SystemPrompt: "Work.", Tools: []string{"read_file"}, StrictTools: true,
	}
	runtime := &fakeScoutTools{available: []client.Tool{{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "read_file"}}}}
	result, err := (GeneralRunner{
		Client: configured, Tools: runtime,
		Spec:       Spec{Role: config.AgentRoleScout, Model: "model", ContextLength: 16_000, Candidates: []client.ModelCandidate{{Model: "model"}}},
		Definition: definition, Resume: &state,
	}).Run(t.Context(), "do work")
	if err != nil || result.Outcome != "succeeded" || modelRequests != 1 {
		t.Fatalf("result=%#v err=%v modelRequests=%d", result, err, modelRequests)
	}
}
