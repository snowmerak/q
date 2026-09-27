package app

import (
	"context"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	acp "github.com/snowmerak/q/third_party/acp-go-sdk"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/tools/builtin"
	"github.com/snowmerak/q/workspace"
)

type customSkillCatalogTools struct {
	*fakeAgentTools
	catalog []client.Tool
}

func (r *customSkillCatalogTools) CustomTools() []client.Tool { return r.catalog }

func TestCustomCatalogForwardsDelegatedSkillHintSearch(t *testing.T) {
	base := &customSkillCatalogTools{
		fakeAgentTools: &fakeAgentTools{skillHintResults: []qtools.SkillHintSearchResult{{
			Hits: []builtin.SkillSearchHit{{ID: "skill-go", Title: "go-review"}},
		}}},
		catalog: []client.Tool{
			{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "search_skills"}},
			{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "get_skill"}},
		},
	}
	m := model{hostState: hostState{toolRuntime: base}}
	runtime := &delegationRuntime{base: m.customTools()}
	hints, err := runtime.SearchSkillHints(t.Context(), "inspect Go tests", 8)
	if err != nil || len(hints.Hits) != 1 || hints.Hits[0].ID != "skill-go" ||
		len(base.skillHintQueries) != 1 || base.skillHintQueries[0] != "inspect Go tests" {
		t.Fatalf("hints=%#v err=%v queries=%#v", hints, err, base.skillHintQueries)
	}

	base.catalog = nil
	if _, err := runtime.SearchSkillHints(context.Background(), "hidden skill", 8); err == nil {
		t.Fatal("skill search succeeded after removal from custom tool catalog")
	}
}

func TestCustomUsesSessionMCPCatalog(t *testing.T) {
	tool := client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "mcp_docs__read"}}
	base := &roleCatalogTools{toolsByRole: map[string][]client.Tool{"default": {tool}}}
	m := model{hostState: hostState{toolRuntime: base}}
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

func TestCustomInfoShowsExactCanonicalProfile(t *testing.T) {
	m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
	workspaceStore := workspace.Store{Root: t.TempDir()}
	m.workspaceStore = &workspaceStore
	store := m.customStore()
	for scope, prompt := range map[string]string{"global": "Global prompt", "workspace": "Workspace prompt"} {
		if err := store.Save(subagent.Profile{
			Version: 1, Name: "reader", Role: "scout", SystemPrompt: prompt,
			Tools: []string{}, Delegates: []string{},
		}, scope, nil); err != nil {
			t.Fatal(err)
		}
	}
	global := m.customInfo("/subagents show global/reader")
	workspace := m.customInfo("/subagents show workspace/reader")
	if !strings.Contains(global, "Global prompt") || strings.Contains(global, "Workspace prompt") ||
		!strings.Contains(workspace, "Workspace prompt") || strings.Contains(workspace, "Global prompt") {
		t.Fatalf("global = %q, workspace = %q", global, workspace)
	}
}

func TestCustomACPExecuteAndList(t *testing.T) {
	c := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskStartToolName, `{"objective":"inspect"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"ACP custom result"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskStartToolName, `{"objective":"inspect builtin"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"ACP builtin result"}`)}},
	}}
	agent, ws, connection := testACPAgent(t, c, &fakeAgentTools{})
	agent.state.config.Provider.Model = "plan-model"
	profiles := subagent.ProfileStore{Workspace: ws.Root + "/.q/subagents"}
	if err := profiles.Save(subagent.Profile{Version: 1, Name: "inspector", Role: "scout", SystemPrompt: "ACP profile prompt", Tools: []string{}}, "workspace", nil); err != nil {
		t.Fatal(err)
	}
	id := openTestACPSession(t, agent, ws.Root)
	for _, command := range []string{
		"/subagents list", "/subagents show inspector", "/subagents show builtin/scout",
		"/subagent inspector explicit context", "/subagent builtin/scout inspect builtin context",
	} {
		response, err := agent.Prompt(t.Context(), acp.PromptRequest{SessionId: id, Prompt: []acp.ContentBlock{acp.TextBlock(command)}})
		if err != nil || response.StopReason != acp.StopReasonEndTurn {
			t.Fatalf("%s: %+v %v", command, response, err)
		}
	}
	if len(c.requests) != 4 || len(c.requests[0].Messages) != 2 ||
		!strings.HasPrefix(c.requests[0].Messages[0].Content, "ACP profile prompt\n\nRuntime environment:") {
		t.Fatal(c.requests)
	}
	var output string
	for _, n := range connection.snapshot() {
		if u := n.Update.AgentMessageChunk; u != nil && u.Content.Text != nil {
			output += u.Content.Text.Text
		}
	}
	if !strings.Contains(output, "ACP custom result") || !strings.Contains(output, "ACP builtin result") ||
		!strings.Contains(output, "inspector") || !strings.Contains(output, subagent.BuiltinScoutID) {
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
	c := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskStartToolName, `{"objective":"inspect"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"custom result"}`)}},
	}}
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
	if len(c.requests) != 2 || len(c.requests[0].Messages) != 2 ||
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
	m.custom.inputs[customFieldRole].SetValue("analyst")
	m.custom.prompt.SetValue("First\nSecond")
	updated, _ = m.saveCustom()
	m = updated.(model)
	e, err := m.customStore().Get("inspector")
	if err != nil || e.Profile.SystemPrompt != "First\nSecond" {
		t.Fatalf("%+v %v %s", e, err, m.status)
	}
	index, found := m.custom.selectedProfileIndex()
	if !found || m.custom.entries[index].Profile.Name != "inspector" {
		t.Fatal("saved profile was not selected")
	}
	m.custom.cursor = len(m.custom.fixed)
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
