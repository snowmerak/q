package app

import (
	"context"
	"strings"
	"testing"

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

func TestStreamAgentSearchReturnsACPReport(t *testing.T) {
	var received subagent.ExternalSearchInput
	search := func(_ context.Context, input subagent.ExternalSearchInput) (subagent.ExternalSearchResult, error) {
		received = input
		return subagent.ExternalSearchResult{Agent: "grok", Summary: "Evidence report"}, nil
	}
	events := make(chan agentEvent, 4)
	streamAgentSearch(t.Context(), search, "latest API behavior", events)

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
		activities[1].Action != subagent.ProgressCompleted || responseText != "Evidence report" {
		t.Fatalf("input = %#v, activities = %#v, response = %q", received, activities, responseText)
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
