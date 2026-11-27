package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func pumpAgentToIdle(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	m, _ = pumpAgentUntil(t, m, cmd, "turn completion", func(m model) bool { return !m.waiting })
	return m
}

func pumpAgentToStatus(t *testing.T, m model, cmd tea.Cmd, status string) (model, tea.Cmd) {
	t.Helper()
	return pumpAgentUntil(t, m, cmd, status, func(m model) bool { return strings.Contains(m.status, status) })
}

func pumpAgentUntil(t *testing.T, m model, cmd tea.Cmd, want string, reached func(model) bool) (model, tea.Cmd) {
	t.Helper()
	const maxSteps = 128
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var events []string
	fail := func(reason string) {
		t.Helper()
		t.Fatalf("waiting for %q: %s (status=%q waiting=%v asking=%v turn error=%v events=%q)",
			want, reason, m.status, m.waiting, m.asking, m.turnErr, events)
	}
	for !reached(m) {
		if !m.waiting || cmd == nil {
			fail("turn has no pending command")
		}
		if len(events) == maxSteps {
			fail("turn event limit reached")
		}
		result := make(chan tea.Msg, 1)
		go func(cmd tea.Cmd) { result <- nextTurnMessage(cmd) }(cmd)
		select {
		case <-ctx.Done():
			fail(ctx.Err().Error())
		case msg := <-result:
			if msg == nil {
				fail("command did not produce a turn message")
			}
			if agent, ok := msg.(agentEventMsg); ok {
				events = append(events, summarizeAgentEvent(agent.event))
			} else {
				events = append(events, fmt.Sprintf("%T", msg))
			}
			updated, next := m.Update(msg)
			m, cmd = updated.(model), next
		}
	}
	return m, cmd
}

// Visit turn commands while ignoring UI-only messages such as spinner ticks.
func nextTurnMessage(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case agentEventMsg, chatResultMsg, compactionResultMsg:
		return msg
	case tea.BatchMsg:
		for _, child := range msg {
			if turn := nextTurnMessage(child); turn != nil {
				return turn
			}
		}
	}
	return nil
}

func summarizeAgentEvent(event agentEvent) string {
	switch {
	case event.err != nil:
		return "error"
	case event.question != nil:
		return "question"
	case event.taskStarted != nil:
		return "task started"
	case event.taskCompleted:
		return "task completed"
	case event.promptUsage != nil:
		return "prompt usage"
	case event.toolTokenEstimate > 0:
		return "tool token estimate"
	case event.call != nil:
		return "call " + event.call.Function.Name
	case event.message != nil:
		return strings.TrimSpace(string(event.message.Role) + " " + event.message.Name)
	case event.compaction != nil:
		return "compaction"
	case event.complete:
		return "complete"
	case event.status != "":
		return event.status
	default:
		return "internal event"
	}
}
