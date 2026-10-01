package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/subagent"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/tools/builtin"
	"github.com/snowmerak/q/workspace"
)

// Persist immediately to reproduce indexing winning the race with tool dispatch.
type immediateArchive struct{ *sessionstore.Store }

func (a immediateArchive) Append(record sessionstore.Record) error {
	_, err := a.Save(record)
	return err
}

func (a immediateArchive) Flush() error { return nil }

func TestChatAndSubagentSearchOmitTheirJustArchivedResponse(t *testing.T) {
	for _, child := range []bool{false, true} {
		name := "root"
		if child {
			name = "child"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			archive, err := sessionstore.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = archive.Close() })
			runtime, err := qtools.NewRuntimeWithArchive(t.Context(), root, archive)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = runtime.Close() })
			taskID := ""
			if child {
				taskID = "child"
			}
			if _, err := archive.Save(sessionstore.Record{
				ID: "past", Kind: sessionstore.KindMessage, RunID: "run", TaskID: taskID,
				Role: "assistant", Content: "unique archive phrase", CreatedAt: time.Now().UTC().Add(-time.Hour),
			}); err != nil {
				t.Fatal(err)
			}
			search := client.Message{Role: client.RoleAssistant, Content: "unique archive phrase", ToolCalls: []client.ToolCall{{
				ID: "search", Type: client.ToolTypeFunction, Function: client.FunctionCall{
					Name: "search_archive", Arguments: `{"query":"unique archive phrase","kinds":["message"],"limit":1}`,
				},
			}}}
			configured := &extractionClient{}
			var result client.Message
			if child {
				configured.responses = []client.Message{
					{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "start", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "task_start", Arguments: `{"objective":"inspect"}`}}}},
					search,
					{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: "complete", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: "task_complete", Arguments: `{"outcome":"succeeded","summary":"done"}`}}}},
				}
				var state subagent.GeneralRunState
				runner := subagent.GeneralRunner{
					Client: configured, Tools: runtime, RunID: "run", TaskID: taskID, Sink: immediateArchive{archive},
					Spec: subagent.Spec{Role: config.AgentRoleResearch, Model: "model", Candidates: []client.ModelCandidate{{Model: "model"}}},
					Definition: subagent.AgentDefinition{
						Info:         subagent.DelegateInfo{Name: "workspace/worker", Kind: subagent.AgentKindInner, Role: config.AgentRoleResearch},
						SystemPrompt: "Work.", Tools: []string{"search_archive"}, StrictTools: true,
					},
					Checkpoint: func(saved subagent.GeneralRunState) error { state = saved; return nil },
				}
				// The child must override the inherited root scope.
				if _, err := runner.Run(sessionstore.WithSearchScope(t.Context(), "run", ""), "inspect"); err != nil {
					t.Fatal(err)
				}
				for _, message := range state.Transcript {
					if message.Role == client.RoleTool && message.Name == "search_archive" {
						result = message
					}
				}
			} else {
				configured.responses = []client.Message{search, {Role: client.RoleAssistant, Content: "done"}}
				m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
				value := config.Default()
				value.Provider.Model = "model"
				m.enterChat(value, configured)
				m.runID, m.archive, m.toolRuntime = "run", immediateArchive{archive}, runtime
				m.workspaceStore = &workspace.Store{Root: root}
				first, ok := m.sendChatRequest()().(agentEventMsg)
				if !ok {
					t.Fatal("chat did not enter the agent loop")
				}
				consume := func(event agentEvent) {
					if event.err != nil {
						t.Fatal(event.err)
					}
					if event.message != nil {
						m.archiveMessage(*event.message, sessionstore.StatusSucceeded, event.toolIsError)
						if event.message.Role == client.RoleTool && event.message.Name == "search_archive" {
							result = *event.message
						}
					}
					if event.persistenceAck != nil {
						close(event.persistenceAck)
					}
				}
				consume(first.event)
				for event := range first.events {
					consume(event)
				}
				if m.archiveErr != nil {
					t.Fatal(m.archiveErr)
				}
			}
			var receipt struct {
				Result builtin.SearchArchiveOutput `json:"result"`
			}
			if err := json.Unmarshal([]byte(result.TextContent()), &receipt); err != nil {
				t.Fatalf("search result %q: %v", result.TextContent(), err)
			}
			if receipt.Result.Total != 1 || len(receipt.Result.Hits) != 1 || receipt.Result.Hits[0].ID != "past" {
				t.Fatalf("search returned its own response: %#v", receipt.Result)
			}
		})
	}
}
