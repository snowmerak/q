package app

import (
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	acp "github.com/snowmerak/q/third_party/acp-go-sdk"
	"github.com/snowmerak/q/workspace"
)

func TestCustomUsesSessionMCPCatalog(t *testing.T) {
	tool := client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "mcp_docs__read"}}
	base := &roleCatalogTools{toolsByRole: map[string][]client.Tool{"default": {tool}}}
	m := model{toolRuntime: base}
	p := subagent.Profile{Version: 1, Name: "reader", Role: "scout", SystemPrompt: "Read", Tools: []string{tool.Function.Name}}
	selected, err := subagent.SelectCustomTools(p, m.customTools())
	if err != nil || len(selected) != 1 {
		t.Fatalf("%+v %v", selected, err)
	}
	if _, err = m.customTools().Call(t.Context(), client.ToolCall{Function: client.FunctionCall{Name: tool.Function.Name}}); err != nil {
		t.Fatal(err)
	}
	if len(base.calls) != 1 {
		t.Fatal("MCP call not forwarded")
	}
}

func TestCustomACPExecuteAndList(t *testing.T) {
	c := &planningClient{responses: []client.Message{{Role: client.RoleAssistant, Content: "ACP custom result"}}}
	agent, ws, connection := testACPAgent(t, c, &fakeAgentTools{})
	agent.state.config.Provider.Model = "plan-model"
	profiles := subagent.ProfileStore{Workspace: ws.Root + "/.q/subagents"}
	if err := profiles.Save(subagent.Profile{Version: 1, Name: "inspector", Role: "scout", SystemPrompt: "ACP profile prompt", Tools: []string{}}, "workspace", nil); err != nil {
		t.Fatal(err)
	}
	id := openTestACPSession(t, agent, ws.Root)
	for _, command := range []string{"/subagents list", "/subagents show inspector", "/subagent inspector explicit context"} {
		response, err := agent.Prompt(t.Context(), acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock(command)}})
		if err != nil || response.StopReason != acp.StopReasonEndTurn {
			t.Fatalf("%s: %+v %v", command, response, err)
		}
	}
	if len(c.requests) != 1 || len(c.requests[0].Messages) != 2 ||
		!strings.HasPrefix(c.requests[0].Messages[0].Content, "ACP profile prompt\n\nRuntime environment:") {
		t.Fatal(c.requests)
	}
	var output string
	for _, n := range connection.snapshot() {
		if u := n.Update.AgentMessageChunk; u != nil && u.Content.Text != nil {
			output += u.Content.Text.Text
		}
	}
	if !strings.Contains(output, "ACP custom result") || !strings.Contains(output, "inspector") {
		t.Fatal(output)
	}
}

func TestCustomEditorShowsStatusWithoutLosingDraft(t *testing.T) {
	s := config.Store{Dir: t.TempDir()}
	v := config.Default()
	v.Provider.Model = "test"
	if err := s.Save(v); err != nil {
		t.Fatal(err)
	}
	m := newModel(t.Context(), s, nil)
	m.config = v
	m.resize(100, 30)
	updated, _ := m.enterCustom()
	m = updated.(model)
	updated, _ = m.beginCustomEdit(true)
	m = updated.(model)
	m.custom.prompt.SetValue("keep draft")
	m.status = "validation error"
	if !strings.Contains(m.viewCustom(), "validation error") || m.custom.prompt.Value() != "keep draft" {
		t.Fatal("status hidden")
	}
	if lines := strings.Count(m.viewCustom(), "\n"); lines >= 30 {
		t.Fatalf("editor overflows: %d", lines)
	}
}

func TestCustomTUIExecute(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "plan-model"
	value.Agents.Roles = map[string]config.AgentConfig{"analyst": {}}
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	store := workspace.Store{Root: t.TempDir()}
	m.workspaceStore = &store
	m.toolRuntime = &fakeAgentTools{}
	c := &planningClient{responses: []client.Message{{Role: client.RoleAssistant, Content: "custom result"}}}
	m.enterChat(value, c)
	m.resize(100, 36)
	if err := m.customStore().Save(subagent.Profile{Version: 1, Name: "inspector", Role: "analyst", SystemPrompt: "custom prompt", Tools: []string{}}, "workspace", nil); err != nil {
		t.Fatal(err)
	}
	m.input.SetValue("/subagent inspector inspect this")
	updated, cmd := m.submitChat()
	m = updated.(model)
	if !m.waiting {
		t.Fatal(m.status)
	}
	for i := 0; m.waiting && i < 64; i++ {
		updated, cmd = m.Update(nextAgentMessage(t, cmd))
		m = updated.(model)
	}
	if m.waiting || m.messages[len(m.messages)-1].Content != "custom result" {
		t.Fatalf("%s %+v", m.status, m.messages)
	}
	if len(c.requests) != 1 || len(c.requests[0].Messages) != 2 ||
		!strings.HasPrefix(c.requests[0].Messages[0].Content, "custom prompt\n\nRuntime environment:") {
		t.Fatal(c.requests)
	}
}
func TestCustomTUIManageProfile(t *testing.T) {
	s := config.Store{Dir: t.TempDir()}
	v := config.Default()
	v.Provider.Model = "test"
	v.Agents.Roles = map[string]config.AgentConfig{"analyst": {}}
	if err := s.Save(v); err != nil {
		t.Fatal(err)
	}
	m := newModel(t.Context(), s, nil)
	m.config = v
	m.resize(100, 36)
	updated, _ := m.enterCustom()
	m = updated.(model)
	updated, _ = m.beginCustomEdit(true)
	m = updated.(model)
	m.custom.inputs[0].SetValue("inspector")
	m.custom.inputs[3].SetValue("analyst")
	m.custom.prompt.SetValue("First\nSecond")
	updated, _ = m.saveCustom()
	m = updated.(model)
	e, err := m.customStore().Get("inspector")
	if err != nil || e.Profile.SystemPrompt != "First\nSecond" {
		t.Fatalf("%+v %v %s", e, err, m.status)
	}
	if m.custom.entries[m.custom.cursor].Profile.Name != "inspector" {
		t.Fatal("saved profile was not selected")
	}
	m.custom.cursor = 0
	updated, _ = m.beginCustomEdit(false)
	m = updated.(model)
	m.custom.prompt.SetValue("Updated")
	updated, _ = m.saveCustom()
	m = updated.(model)
	e, err = m.customStore().Get("inspector")
	if err != nil || e.Profile.SystemPrompt != "Updated" {
		t.Fatal(err, m.status)
	}
	updated, _ = m.deleteCustom()
	m = updated.(model)
	if _, err = m.customStore().Get("inspector"); err == nil {
		t.Fatal("profile not deleted")
	}
}
