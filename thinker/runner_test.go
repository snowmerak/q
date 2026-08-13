package thinker

import (
	"context"
	"errors"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/subagent"
)

type fakeThinkerClient struct {
	responses []client.Message
	requests  []client.ChatRequest
}

func (f *fakeThinkerClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	f.requests = append(f.requests, request)
	if len(f.responses) == 0 {
		return nil, errors.New("no thinker response")
	}
	message := f.responses[0]
	f.responses = f.responses[1:]
	return &client.ChatResponse{
		Choices: []client.Choice{{Message: message}},
		Usage:   client.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
	}, nil
}

type fakePropositionLibrary struct {
	keys   []string
	inputs []qlibrary.PropositionRegisterRequest
}

func (f *fakePropositionLibrary) RegisterProposition(_ context.Context, key string, input qlibrary.PropositionRegisterRequest) (qlibrary.PropositionRegisterResponse, error) {
	f.keys = append(f.keys, key)
	f.inputs = append(f.inputs, input)
	return qlibrary.PropositionRegisterResponse{ID: "prop-" + key, Created: true}, nil
}

func TestRunnerRegistersOnePropositionPerRoundAndCompletes(t *testing.T) {
	configuredClient := &fakeThinkerClient{responses: []client.Message{
		thinkerToolCall("register-1", RegisterToolName, `{"content":"First fact.","queries":["first query"],"confidence":0.9,"tags":["test"]}`),
		thinkerToolCall("register-2", RegisterToolName, `{"content":"Second fact.","queries":["second query"],"confidence":0.8,"tags":[]}`),
		thinkerToolCall("complete", CompleteToolName, `{}`),
	}}
	library := &fakePropositionLibrary{}
	result, err := (Runner{
		Client: configuredClient, Library: library,
		Spec: subagent.Spec{Role: config.AgentRoleThinker, Model: "thinker-model", ContextLength: 16_000},
	}).Run(context.Background(), Job{
		ID: "job-1", Messages: []client.Message{{Role: client.RoleUser, Content: "Remember two facts."}},
		Refs: []string{"run:one"}, WorkingDirectory: `C:\workspace`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Registered != 2 || len(result.IDs) != 2 || result.Usage.TotalTokens != 36 {
		t.Fatalf("result = %#v", result)
	}
	if len(library.keys) != 2 || library.keys[0] != "job-1/0" || library.keys[1] != "job-1/1" {
		t.Fatalf("idempotency keys = %#v", library.keys)
	}
	if library.inputs[0].ExtractorModel != "thinker-model" || library.inputs[0].ExtractorVersion != ExtractorVersion || library.inputs[0].Refs[0] != "run:one" {
		t.Fatalf("registration metadata = %#v", library.inputs[0])
	}
	if len(configuredClient.requests) != 3 || configuredClient.requests[0].ParallelToolCalls == nil || *configuredClient.requests[0].ParallelToolCalls {
		t.Fatalf("requests = %#v", configuredClient.requests)
	}
	if configuredClient.requests[1].Messages[len(configuredClient.requests[1].Messages)-1].Role != client.RoleTool {
		t.Fatalf("registration ACK was not fed back: %#v", configuredClient.requests[1].Messages)
	}
}

func TestRunnerRejectsMultipleToolCallsInOneRound(t *testing.T) {
	message := thinkerToolCall("one", RegisterToolName, `{"content":"One.","queries":[],"confidence":1,"tags":[]}`)
	message.ToolCalls = append(message.ToolCalls, client.ToolCall{
		ID: "two", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: CompleteToolName, Arguments: `{}`},
	})
	_, err := (Runner{
		Client: &fakeThinkerClient{responses: []client.Message{message}}, Library: &fakePropositionLibrary{},
		Spec: subagent.Spec{Role: config.AgentRoleThinker, Model: "thinker-model", ContextLength: 4000},
	}).Run(context.Background(), Job{ID: "job", Messages: []client.Message{{Role: client.RoleUser, Content: "fact"}}})
	if err == nil {
		t.Fatal("multiple tool calls were accepted")
	}
}

func thinkerToolCall(id, name, arguments string) client.Message {
	return client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
		ID: id, Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: name, Arguments: arguments},
	}}}
}
