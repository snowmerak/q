package app

import (
	"strings"
	"testing"

	"github.com/snowmerak/q/config"
)

func TestRetiredPlanCommandsDoNotStartChatTurns(t *testing.T) {
	for _, item := range localSlashCommands {
		if retiredPlanCommand(item.name) {
			t.Fatalf("retired command %q remains in completion", item.name)
		}
	}
	for _, command := range []string{"/plan", "/plan implement this", "/auto-approve on", "/auto-resolve", "/autonomous on"} {
		m := newModel(t.Context(), config.Store{}, nil)
		client := &fakeClient{}
		m.client = client
		m.input.SetValue(command)
		updated, _ := m.submitChat()
		state := updated.(model)
		if state.waiting || len(state.messages) != 0 || !strings.Contains(state.status, "Plan mode was removed") {
			t.Fatalf("command %q started work: waiting=%v messages=%d status=%q", command, state.waiting, len(state.messages), state.status)
		}
	}
}

func TestRetiredPlanCommandsAreHandledByACPWithoutModelCall(t *testing.T) {
	agent, store, _ := testACPAgent(t, &fakeClient{}, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, store.Root)
	runtime := activeACPRuntime(t, agent, sessionID)
	for _, command := range []string{"/plan implement this", "/auto-approve on", "/auto-resolve", "/autonomous"} {
		_, handled, err := runtime.runACPCommand(t.Context(), command)
		if err != nil || !handled {
			t.Fatalf("command %q: handled=%v err=%v", command, handled, err)
		}
	}
	if requests := runtime.state.client.(*fakeClient).requests; len(requests) != 0 {
		t.Fatalf("retired commands called model: %d requests", len(requests))
	}
}
