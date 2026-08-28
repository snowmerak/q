package thinker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/subagent"
)

type fakeThinkerClient struct {
	responses        []client.Message
	requests         []client.ChatRequest
	terminalRequests []client.ChatRequest
	terminalUsage    client.Usage
	terminalErr      error
}

func (f *fakeThinkerClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	if request.ToolChoice == client.ToolChoiceNone && len(request.Messages) > 0 && request.Messages[len(request.Messages)-1].Role == client.RoleTool {
		f.terminalRequests = append(f.terminalRequests, request)
		return &client.ChatResponse{Usage: f.terminalUsage, Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant}, FinishReason: "stop"}}}, f.terminalErr
	}
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

type dispositionPropositionLibrary struct {
	responses []qlibrary.PropositionRegisterResponse
}

type failedThenCreatedPropositionLibrary struct {
	keys  []string
	calls int
}

func (f *failedThenCreatedPropositionLibrary) RegisterProposition(
	_ context.Context,
	key string,
	_ qlibrary.PropositionRegisterRequest,
) (qlibrary.PropositionRegisterResponse, error) {
	f.keys = append(f.keys, key)
	f.calls++
	if f.calls <= 2 {
		return qlibrary.PropositionRegisterResponse{}, errors.New("temporary registration failure")
	}
	return qlibrary.PropositionRegisterResponse{ID: "prop-next", Action: qlibrary.PropositionActionCreate, Created: true}, nil
}

func (f *dispositionPropositionLibrary) RegisterProposition(
	_ context.Context,
	_ string,
	_ qlibrary.PropositionRegisterRequest,
) (qlibrary.PropositionRegisterResponse, error) {
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
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
	if result.Proposed != 2 || result.Processed != 2 || result.Registered != 2 || result.Created != 2 || result.Merged != 0 || result.Discarded != 0 ||
		len(result.IDs) != 2 || result.Usage.TotalTokens != 36 {
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
	if len(configuredClient.terminalRequests) != 1 {
		t.Fatalf("completion was not returned upstream: %#v", configuredClient.terminalRequests)
	}
	terminal := configuredClient.terminalRequests[0]
	last := terminal.Messages[len(terminal.Messages)-1]
	if last.ToolCallID != "complete" || last.Name != CompleteToolName || !strings.Contains(last.Content, `"registered":2`) {
		t.Fatalf("completion result = %#v", last)
	}
}

func TestRunnerWaitsForCompletionDeliveryAndCountsItsUsage(t *testing.T) {
	for _, failed := range []bool{false, true} {
		fake := &fakeThinkerClient{responses: []client.Message{thinkerToolCall("complete", CompleteToolName, `{}`)}, terminalUsage: client.Usage{TotalTokens: 5}}
		failure := errors.New("completion delivery failed")
		if failed {
			fake.terminalErr = failure
		}
		result, err := (Runner{Client: fake, Library: &fakePropositionLibrary{}, Spec: subagent.Spec{Role: config.AgentRoleThinker, Model: "thinker", ContextLength: 16_000}}).Run(t.Context(), Job{
			ID: "job", Messages: []client.Message{{Role: client.RoleUser, Content: "No new facts."}},
		})
		if len(fake.terminalRequests) != 1 || (failed && !errors.Is(err, failure)) || (!failed && (err != nil || result.Usage.TotalTokens != 17)) {
			t.Fatalf("failed=%v result=%#v err=%v terminalRequests=%d", failed, result, err, len(fake.terminalRequests))
		}
	}
}

func TestRunnerCompactsWhilePreservingSourceAndAcknowledgedPropositions(t *testing.T) {
	registration := thinkerToolCall("register", RegisterToolName, `{"content":"Keep the exact durable fact.","queries":[],"confidence":0.9,"tags":[]}`)
	registration.Content = strings.Repeat("x", 40_000)
	fake := &fakeThinkerClient{responses: []client.Message{
		registration,
		{Role: client.RoleAssistant, Content: "The durable fact was processed; finish extraction when nothing else remains."},
		thinkerToolCall("complete", CompleteToolName, `{}`),
	}}
	library := &fakePropositionLibrary{}
	result, err := (Runner{
		Client: fake, Library: library,
		Spec: subagent.Spec{Role: config.AgentRoleThinker, Model: "thinker", ContextLength: 16_000},
	}).Run(t.Context(), Job{ID: "compact-job", Messages: []client.Message{{Role: client.RoleUser, Content: "Keep the exact durable fact."}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 3 || fake.requests[1].MaxCompletionTokens == nil || result.Registered != 1 || result.Usage.TotalTokens != 36 {
		t.Fatalf("result=%#v requests=%d", result, len(fake.requests))
	}
	initial, resumed := fake.requests[0].Messages, fake.requests[2].Messages
	if initial[0].Content != resumed[0].Content || initial[1].Content != resumed[1].Content {
		t.Fatal("extraction contract or original source was summarized")
	}
	for _, exact := range []string{"Keep the exact durable fact.", "prop-compact-job/0", `"action":"create"`} {
		if !strings.Contains(resumed[2].Content, exact) {
			t.Fatalf("acknowledged proposition lost %q: %s", exact, resumed[2].Content)
		}
	}
	if len(resumed) != 4 || !strings.HasPrefix(resumed[3].Content, "Compressed conversation memory:") {
		t.Fatalf("old extraction history was not compacted: %#v", resumed)
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

func TestRunnerCountsLibraryMergeAndDiscardDecisions(t *testing.T) {
	configuredClient := &fakeThinkerClient{responses: []client.Message{
		thinkerToolCall("merge", RegisterToolName, `{"content":"Existing fact.","queries":[],"confidence":0.9,"tags":[]}`),
		thinkerToolCall("discard", RegisterToolName, `{"content":"Redundant fact.","queries":[],"confidence":0.8,"tags":[]}`),
		thinkerToolCall("complete", CompleteToolName, `{}`),
	}}
	library := &dispositionPropositionLibrary{responses: []qlibrary.PropositionRegisterResponse{
		{ID: "prop-existing", Action: qlibrary.PropositionActionMerge, Merged: true},
		{ID: "prop-redundant", Action: qlibrary.PropositionActionDiscard, Discarded: true},
	}}
	result, err := (Runner{
		Client: configuredClient, Library: library,
		Spec: subagent.Spec{Role: config.AgentRoleThinker, Model: "thinker-model", ContextLength: 16_000},
	}).Run(context.Background(), Job{
		ID: "job-decisions", Messages: []client.Message{{Role: client.RoleUser, Content: "Remember facts."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Proposed != 2 || result.Processed != 2 || result.Registered != 1 || result.Created != 0 || result.Merged != 1 || result.Discarded != 1 ||
		len(result.IDs) != 1 || result.IDs[0] != "prop-existing" {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunnerConsumesIdempotencySlotAfterFailedProposal(t *testing.T) {
	configuredClient := &fakeThinkerClient{responses: []client.Message{
		thinkerToolCall("failed", RegisterToolName, `{"content":"Failed proposal.","queries":[],"confidence":0.9,"tags":[]}`),
		thinkerToolCall("next", RegisterToolName, `{"content":"Next proposal.","queries":[],"confidence":0.8,"tags":[]}`),
		thinkerToolCall("complete", CompleteToolName, `{}`),
	}}
	library := &failedThenCreatedPropositionLibrary{}
	result, err := (Runner{
		Client: configuredClient, Library: library,
		Spec: subagent.Spec{Role: config.AgentRoleThinker, Model: "thinker-model", ContextLength: 16_000},
	}).Run(context.Background(), Job{
		ID: "job-slots", Messages: []client.Message{{Role: client.RoleUser, Content: "Remember facts."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Proposed != 2 || result.Processed != 1 || len(library.keys) != 3 ||
		library.keys[0] != "job-slots/0" || library.keys[1] != "job-slots/0" || library.keys[2] != "job-slots/1" {
		t.Fatalf("result = %#v, keys = %#v", result, library.keys)
	}
}

func thinkerToolCall(id, name, arguments string) client.Message {
	return client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{
		ID: id, Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: name, Arguments: arguments},
	}}}
}
