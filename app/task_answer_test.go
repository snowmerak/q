package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	acp "github.com/snowmerak/q/third_party/acp-go-sdk"
	"github.com/snowmerak/q/workspace"
)

const taskAnswer = "## Inspection results\n\nThe Dockerfile uses a pinned base image. Evidence: Dockerfile:1."

func answeringTerminalClient(placement string) *parkedTerminalClient {
	p := &parkedTerminalClient{finalReply: taskAnswer, replay: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"retained-private-state"}`)}}
	if placement == "before" {
		p.beforeReply = taskAnswer
	}
	if placement == "alongside" {
		p.completeReply = taskAnswer
	}
	return p
}

func assertTaskAnswerSaved(t *testing.T, store workspace.Store, internalAck bool) {
	t.Helper()
	saved, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, message := range saved.Transcript {
		if message.Role == client.RoleAssistant && message.TextContent() == taskAnswer {
			count++
		}
		if strings.Contains(message.TextContent(), "Old task acknowledgment") || strings.Contains(message.TextContent(), "still needs task_complete") {
			t.Fatal("model-only history leaked into saved transcript")
		}
	}
	if count != 1 || saved.ActiveTask != nil {
		t.Fatalf("answer count=%d, active task=%+v", count, saved.ActiveTask)
	}
	last := saved.Context[len(saved.Context)-1]
	if internalAck && !strings.Contains(last.TextContent(), "Old task acknowledgment") {
		t.Fatalf("provider context lost acknowledgment: %+v", last)
	}
	if len(saved.ResponseReplay) == 0 {
		t.Fatal("terminal response lost private replay state")
	}
}

func TestTaskAnswersPersistOnceAndRetainProviderContext(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, placement := range []string{"before", "alongside", "after"} {
			t.Run(fmt.Sprintf("stream=%v/%s", streaming, placement), func(t *testing.T) {
				p := answeringTerminalClient(placement)
				value := config.Default()
				value.Provider.Model = "test/model"
				m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
				m.toolRuntime = &fakeAgentTools{}
				m.enterChat(value, p)
				store := workspace.Store{Root: t.TempDir()}
				m.workspaceStore = &store
				if streaming {
					m.gatewayConfig = gateway.Config{Providers: []gateway.ProviderConfig{{ID: "test", Type: "codex", Enabled: true}}}
				}
				m = runTerminalTestTurn(t, m, "Inspect the Dockerfile")
				if m.turnErr != nil || p.pending {
					t.Fatalf("turn error=%v pending=%v", m.turnErr, p.pending)
				}
				assertTaskAnswerSaved(t, store, placement != "after")
				m = runTerminalTestTurn(t, m, "Explain the build")
				if m.turnErr != nil || m.messages[len(m.messages)-1].Content != "New answer: Explain the build" {
					t.Fatalf("follow-up received stale reply: %v %+v", m.turnErr, m.messages)
				}
			})
		}
	}
}

func TestACPTaskAnswersAreVisibleExactlyOnce(t *testing.T) {
	for _, placement := range []string{"before", "alongside", "after"} {
		t.Run(placement, func(t *testing.T) {
			p := answeringTerminalClient(placement)
			agent, store, connection := testACPAgent(t, p, &fakeAgentTools{})
			id := openTestACPSession(t, agent, store.Root)
			if _, err := agent.Prompt(t.Context(), acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock("Inspect the Dockerfile")}}); err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			for _, update := range connection.snapshot() {
				if chunk := update.Update.AgentMessageChunk; chunk != nil && chunk.Content.Text != nil {
					output.WriteString(chunk.Content.Text.Text)
				}
			}
			if strings.Count(output.String(), taskAnswer) != 1 || strings.Contains(output.String(), "Old task acknowledgment") {
				t.Fatalf("duplicate or missing ACP answer: %q", output.String())
			}
			assertTaskAnswerSaved(t, *activeACPRuntime(t, agent, id).state.workspaceStore, placement != "after")
		})
	}
}
