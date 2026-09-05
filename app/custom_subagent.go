package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/subagent"
	acp "github.com/snowmerak/q/third_party/acp-go-sdk"
	"gopkg.in/yaml.v3"
)

// Custom profiles select from the normal session catalog, including its connected
// MCP tools. Their model role does not apply a built-in runner's tool filter.
func (m model) customTools() agentToolRuntime {
	if catalog, ok := m.toolRuntime.(interface{ CustomTools() []client.Tool }); ok {
		return customCatalogRuntime{agentToolRuntime: m.toolRuntime, catalog: catalog}
	}
	return scopeTools(m.toolRuntime, mcpconfig.RoleDefault)
}

type customCatalogRuntime struct {
	agentToolRuntime
	catalog interface{ CustomTools() []client.Tool }
}

func (r customCatalogRuntime) Tools() []client.Tool { return r.catalog.CustomTools() }

func (m model) customStore() subagent.ProfileStore {
	s := subagent.ProfileStore{Global: filepath.Join(filepath.Dir(m.store.Path()), "subagents")}
	if m.workspaceStore != nil {
		s.Workspace = filepath.Join(m.workspaceStore.Root, ".q", "subagents")
	}
	return s
}
func customCommand(command string) bool {
	return command == "/subagents" || strings.HasPrefix(command, "/subagents ") || command == "/subagent" || strings.HasPrefix(command, "/subagent ")
}
func parseCustomRun(command string) (string, string, error) {
	fields := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(command, "/subagent")), " ", 2)
	if len(fields) != 2 || strings.TrimSpace(fields[1]) == "" {
		return "", "", fmt.Errorf("Usage: /subagent <name> <request>")
	}
	return fields[0], strings.TrimSpace(fields[1]), nil
}
func (m model) customInfo(command string) string {
	if command == "/subagents" || command == "/subagents list" {
		var b strings.Builder
		b.WriteString("Custom subagents\n")
		for _, e := range m.customStore().List() {
			if e.Shadowed {
				continue
			}
			if e.Err != nil {
				fmt.Fprintf(&b, "%s: %v\n", e.Path, e.Err)
				continue
			}
			fmt.Fprintf(&b, "%s · %s · %s\n  %s\n  %s\n", e.Profile.Name, e.Profile.Role, e.Scope, e.Profile.Description, e.Path)
		}
		return b.String()
	}
	if strings.HasPrefix(command, "/subagents show ") {
		e, err := m.customStore().Get(strings.TrimSpace(strings.TrimPrefix(command, "/subagents show ")))
		if err != nil {
			return err.Error()
		}
		raw, _ := yaml.Marshal(e.Profile)
		return e.Path + "\n" + string(raw)
	}
	return "Usage: /subagents list | /subagents show <name> | /subagent <name> <request>"
}
func (m model) streamCustom(ctx context.Context, name, input string, events chan<- agentEvent) {
	defer close(events)
	fail := func(err error) { emitAgentEvent(ctx, events, agentEvent{err: err}) }
	e, err := m.customStore().Get(name)
	if err != nil {
		fail(err)
		return
	}
	if m.client == nil {
		fail(fmt.Errorf("subagent requires a model"))
		return
	}
	models, err := m.client.ListModels(ctx)
	if err != nil {
		fail(err)
		return
	}
	spec, err := subagent.Resolve(m.activeConfig(), e.Profile.Role, models)
	if err != nil {
		fail(err)
		return
	}
	root, environment := "", ""
	if m.workspaceStore != nil {
		root = m.workspaceStore.Root
	}
	if m.toolRuntime != nil {
		environment = fmt.Sprintf("%+v", m.toolRuntime.Environment())
	}
	runner := subagent.CustomRunner{Client: m.client, Tools: m.customTools(), Spec: spec, Profile: e.Profile, Source: e.Path, WorkingDirectory: root, Environment: environment, Sink: m.archive, RunID: m.runID,
		Progress: func(p subagent.ProgressEvent) {
			activity := agentActivity{Agent: p.Agent, TaskID: p.TaskID, ParentID: p.ParentID, Action: p.Action, Detail: p.Detail}
			emitAgentEvent(ctx, events, agentEvent{activity: &activity})
		},
		Trace: func(t subagent.TraceEvent) {
			trace := agentTrace{Agent: t.Agent, TaskID: t.TaskID, ParentID: t.ParentID, Kind: t.Kind, CallID: t.CallID, Name: t.Name, Content: t.Content, IsError: t.IsError}
			emitAgentEvent(ctx, events, agentEvent{trace: &trace})
		}}
	output, err := runner.Run(ctx, input)
	if err != nil {
		fail(err)
		return
	}
	emitAgentEvent(ctx, events, agentEvent{response: &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, Content: output}}}}})
}
func (m model) startCustom(command string) (tea.Model, tea.Cmd) {
	if command == "/subagents" {
		return m.enterCustom()
	}
	if !strings.HasPrefix(command, "/subagent ") {
		m.input.Reset()
		m.custom.detail = m.customInfo(command)
		m.custom.detailOffset = 0
		m.status = ""
		m.screen = screenCustom
		return m, nil
	}
	name, input, err := parseCustomRun(command)
	if err != nil {
		m.status = err.Error()
		return m, nil
	}
	if _, err = m.customStore().Get(name); err != nil {
		m.status = err.Error()
		return m, nil
	}
	m.planArmed = false
	m.debugArmed = false
	m.beginTurn()
	m.turnMessageStart = len(m.messages)
	m.touchSessionMetadata(input)
	message := client.Message{Role: client.RoleUser, Content: command}
	m.archiveMessage(message, sessionstore.StatusSubmitted, false)
	m.messages = append(m.messages, message)
	if m.memory == nil {
		m.memory = memoryForPlan(m.activeConfig())
	}
	m.memory.Append(message)
	m.pendingMessage = message
	m.input.Reset()
	m.input.Blur()
	m.waiting = true
	m.clearAgentActivities()
	m.status = "Running " + name + "…"
	m.resize(m.width, m.height)
	m.refreshTranscript()
	if err = m.saveWorkspaceSession(); err != nil {
		m.finishTurn()
		m.waiting = false
		m.pendingMessage = client.Message{}
		m.status = err.Error()
		return m, m.input.Focus()
	}
	state := m
	ctx := m.activeTurnContext()
	id := m.turnID
	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		events := make(chan agentEvent)
		go state.streamCustom(ctx, name, input, events)
		return waitAgentEvent(events, id)()
	})
}
func (a *acpAgent) runACPCustom(ctx context.Context, command string) (acp.PromptResponse, error) {
	name, input, err := parseCustomRun(command)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	if _, err = a.state.customStore().Get(name); err != nil {
		return acp.PromptResponse{}, err
	}
	a.state.turnMessageStart = len(a.state.messages)
	changed := a.state.touchSessionMetadata(input)
	message := client.Message{Role: client.RoleUser, Content: command}
	a.state.archiveMessage(message, sessionstore.StatusSubmitted, false)
	a.state.messages = append(a.state.messages, message)
	if a.state.memory == nil {
		a.state.memory = memoryForPlan(a.state.activeConfig())
	}
	a.state.memory.Append(message)
	if err = a.state.saveWorkspaceSession(); err != nil {
		return acp.PromptResponse{}, err
	}
	if err = a.emitSessionInfoContext(ctx, changed); err != nil {
		return acp.PromptResponse{}, err
	}
	if err = a.emitTaskPlanContext(ctx, input, acp.PlanEntryStatusInProgress); err != nil {
		return acp.PromptResponse{}, err
	}
	id, err := sessionstore.NewID()
	if err != nil {
		return acp.PromptResponse{}, err
	}
	trace := newACPPlanTrace(a.root, id, a.updateContext)
	runCtx, cancel := context.WithCancel(ctx)
	events := make(chan agentEvent)
	go a.state.streamCustom(runCtx, name, input, events)
	return a.continueACPPlan(ctx, &acpPlanContinuation{workflowCtx: runCtx, cancel: cancel, events: events, trace: trace, objective: input, workflow: "custom"}, false)
}
