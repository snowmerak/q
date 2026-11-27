package subagent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workitems"
)

func TestLateTicketQuestionWaitsForCompactedCheckpoint(t *testing.T) {
	question := client.ToolCall{ID: "decision", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: ticketQuestionTool, Arguments: `{"question":"Keep the old API?","late":true}`}}
	messages := []client.Message{
		{Role: client.RoleSystem, Content: "Preserve scope"},
		{Role: client.RoleUser, Content: "Check migration"},
		{Role: client.RoleAssistant, Content: strings.Repeat("old discovery evidence ", 5000)},
		{Role: client.RoleUser, Content: "Keep compatibility until we decide"},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{question}},
	}
	spec := Spec{Role: config.AgentRoleResearch, Model: "model", ContextLength: 16000, Candidates: []client.ModelCandidate{{Model: "model"}}}
	checkpoints, compactions := 0, 0
	retryable := errors.New("provider timeout")
	r := GeneralRunner{Spec: spec, Client: contextChatFunc(func(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		if !isContextCheckpointRequest(request) {
			t.Fatal("late decision called model outside compaction")
		}
		compactions++
		if compactions == 1 {
			return nil, retryable
		}
		return contextResponse(contextCheckpointJSON("Summarize migration evidence")), nil
	})}
	state := GeneralRunState{Transcript: messages}
	checkpoint := func() error { checkpoints++; return nil }
	if _, err := r.storeTicketQuestion(t.Context(), &state, NewContextCompactor(spec, messages, nil, 2), question, checkpoint); !errors.Is(err, retryable) || checkpoints != 0 || !PendingTicketQuestion(state.Transcript) {
		t.Fatalf("question escaped unsuccessful compaction: err=%v checkpoints=%d", err, checkpoints)
	}
	result, err := r.storeTicketQuestion(t.Context(), &state, NewContextCompactor(spec, messages, nil, 2), question, checkpoint)
	if err != nil || compactions != 2 || checkpoints != 1 || result.Outcome != workitems.WaitingUser || result.UserQuestion == nil || !result.UserQuestion.Late {
		t.Fatalf("result=%+v err=%v compactions=%d checkpoints=%d", result, err, compactions, checkpoints)
	}
}
