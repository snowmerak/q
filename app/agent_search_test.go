package app

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
)

func TestParseAgentSearchCommand(t *testing.T) {
	tests := []struct {
		command string
		query   string
		handled bool
	}{
		{command: "/agent:search", handled: true},
		{command: "/agent:search ACP session lifecycle", query: "ACP session lifecycle", handled: true},
		{command: "/agent:search-other", handled: false},
		{command: "research ACP", handled: false},
	}
	for _, test := range tests {
		query, handled := parseAgentSearchCommand(test.command)
		if query != test.query || handled != test.handled {
			t.Fatalf("parseAgentSearchCommand(%q) = %q, %v", test.command, query, handled)
		}
	}
}

func TestStreamAgentSearchReturnsParentSynthesis(t *testing.T) {
	var received subagent.ExternalSearchInput
	search := func(_ context.Context, input subagent.ExternalSearchInput) (subagent.ExternalSearchResult, error) {
		received = input
		return subagent.ExternalSearchResult{Agent: "grok", Summary: "Evidence report"}, nil
	}
	parentClient := &fakeClient{}
	events := make(chan agentEvent, 4)
	streamAgentSearch(t.Context(), search, "latest API behavior", agentSearchParent{
		client: parentClient, model: "main-model",
	}, events)

	var activities []agentActivity
	var responseText string
	for event := range events {
		if event.activity != nil {
			activities = append(activities, *event.activity)
		}
		if event.response != nil {
			responseText = event.response.Choices[0].Message.Content
		}
	}
	if received.Query != "latest API behavior" || len(received.CompletionCriteria) == 0 ||
		len(activities) != 2 || activities[0].Action != subagent.ProgressStarted ||
		activities[1].Action != subagent.ProgressCompleted || responseText != "reply 1" {
		t.Fatalf("input = %#v, activities = %#v, response = %q", received, activities, responseText)
	}
	if len(parentClient.requests) != 1 ||
		!strings.Contains(parentClient.requests[0].Messages[len(parentClient.requests[0].Messages)-1].Content, "Evidence report") {
		t.Fatalf("parent requests = %#v", parentClient.requests)
	}
}

func TestAgentSearchParentResponseIsAddedToTUITranscript(t *testing.T) {
	parentClient := &fakeClient{}
	events := make(chan agentEvent, 4)
	streamAgentSearch(t.Context(), func(_ context.Context, _ subagent.ExternalSearchInput) (subagent.ExternalSearchResult, error) {
		return subagent.ExternalSearchResult{Agent: "codex", Summary: "Sourced evidence"}, nil
	}, "explain the evidence", agentSearchParent{client: parentClient, model: "main-model"}, events)

	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(config.Default(), parentClient)
	m.beginTurn()
	m.waiting = true
	user := client.Message{Role: client.RoleUser, Content: "explain the evidence"}
	m.messages = append(m.messages, user)
	m.memory.Append(user)
	for event := range events {
		updated, _ := m.Update(agentEventMsg{event: event})
		m = updated.(model)
	}
	last := m.messages[len(m.messages)-1]
	if m.waiting || last.Role != client.RoleAssistant ||
		last.Content != "reply 1" || !strings.Contains(ansi.Strip(m.viewport.View()), "reply 1") {
		t.Fatalf("waiting=%v messages=%#v transcript=%q", m.waiting, m.messages, m.viewport.View())
	}
}

func TestAgentSearchWithoutAssignmentShowsConfigurationHint(t *testing.T) {
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(config.Default(), &fakeClient{})
	m.input.SetValue("/agent:search current API behavior")
	updated, command := m.submitChat()
	m = updated.(model)
	if command != nil || !strings.Contains(m.status, "Search agent is not configured") {
		t.Fatalf("command = %v, status = %q", command, m.status)
	}
}
