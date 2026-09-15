package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/subagent"
	acp "github.com/snowmerak/q/third_party/acp-go-sdk"
	"github.com/snowmerak/q/workspace"
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

func (m model) resolvePublicAgent(name string) (subagent.AgentDefinition, error) {
	name = strings.TrimSpace(name)
	registry, err := buildSubagentRegistry(m.customStore())
	if err != nil {
		return subagent.AgentDefinition{}, err
	}
	if definition, found := registry.Get(name); found {
		return definition, nil
	}
	entry, err := m.customStore().Get(name)
	if err != nil {
		return subagent.AgentDefinition{}, err
	}
	return subagent.DefinitionForProfile(entry)
}

func (m model) customInfo(command string) string {
	if command == "/subagents" || command == "/subagents list" {
		var b strings.Builder
		b.WriteString("Subagents\n")
		registry, err := buildSubagentRegistry(m.customStore())
		if err != nil {
			return err.Error()
		}
		for _, info := range registry.List() {
			access := "read-only"
			if info.MutatesWorkspace {
				access = "mutates workspace"
			}
			if info.Kind == subagent.AgentKindExternal {
				definition, _ := registry.Get(info.Name)
				if connection, _, available := externalDefinitionConnection(m.activeConfig(), definition); available {
					access += " · ACP " + connection
				} else {
					access += " · unavailable"
				}
			}
			definition, _ := registry.Get(info.Name)
			target := info.Role
			if info.Kind == subagent.AgentKindExternal {
				target = "ACP " + definition.Connection
			}
			fmt.Fprintf(&b, "%s · %s · %s · %s · %s\n  %s\n", info.Name, info.Kind, target, info.Source, access, info.Description)
			if len(definition.Delegates) > 0 {
				fmt.Fprintf(&b, "  delegates: %s\n", strings.Join(definition.Delegates, ", "))
			}
		}
		for _, entry := range m.customStore().List() {
			if entry.Err != nil {
				fmt.Fprintf(&b, "%s: %v\n", entry.Path, entry.Err)
			}
		}
		return b.String()
	}
	if after, ok := strings.CutPrefix(command, "/subagents show "); ok {
		name := strings.TrimSpace(after)
		definition, err := m.resolvePublicAgent(name)
		if err != nil {
			return err.Error()
		}
		if definition.Info.Source == "builtin" {
			connection, _, _ := externalDefinitionConnection(m.activeConfig(), definition)
			raw, _ := yaml.Marshal(map[string]any{
				"name": definition.Info.Name, "description": definition.Info.Description,
				"kind": definition.Info.Kind, "role": definition.Info.Role, "mutates_workspace": definition.Info.MutatesWorkspace,
				"agent": connection, "system_prompt": definition.SystemPrompt,
				"tools": definition.Tools, "delegates": definition.Delegates,
			})
			state := "editable profile"
			if definition.Info.Source == "builtin" {
				state = "builtin"
				if definition.Info.Kind == subagent.AgentKindExternal {
					state += " (ACP binding editable)"
				} else {
					state += " (fixed)"
				}
			}
			return state + "\n" + string(raw)
		}
		profileName := strings.TrimPrefix(definition.Info.Name, definition.Info.Source+"/")
		var entry subagent.ProfileEntry
		found := false
		for _, candidate := range m.customStore().List() {
			if candidate.Scope == definition.Info.Source && candidate.Profile.Name == profileName {
				entry, found = candidate, true
				break
			}
		}
		if !found {
			return fmt.Sprintf("unknown subagent %q", name)
		}
		if entry.Err != nil {
			return entry.Err.Error()
		}
		raw, _ := yaml.Marshal(entry.Profile)
		return entry.Path + "\n" + string(raw)
	}
	return "Usage: /subagents list | /subagents show <name> | /subagent <name> <request>"
}
func (m model) streamCustom(ctx context.Context, name, input string, events chan<- agentEvent) {
	defer close(events)
	fail := func(err error) { emitAgentEvent(ctx, events, agentEvent{err: err}) }
	definition, err := m.resolvePublicAgent(name)
	if err != nil {
		fail(err)
		return
	}
	if definition.Info.Kind == subagent.AgentKindExternal {
		connectionID, connection, available := externalDefinitionConnection(m.activeConfig(), definition)
		if !available {
			fail(fmt.Errorf("subagent %q has no enabled ACP connection", name))
			return
		}
		root := ""
		if m.workspaceStore != nil {
			root = m.workspaceStore.Root
		}
		report, runErr := runACPExternalSubagent(ctx, root, connectionID, connection, definition, input)
		if runErr != nil {
			fail(runErr)
			return
		}
		emitAgentEvent(ctx, events, agentEvent{response: &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, Content: report}}}}})
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
	spec, err := subagent.Resolve(m.activeConfig(), definition.Info.Role, models)
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
	runtime, err := m.configuredDelegationRuntimeFor(m.customTools(), root, definition.Info.Name, []string{definition.Info.Name})
	if err != nil {
		fail(err)
		return
	}
	runner := subagent.GeneralRunner{Client: m.client, Tools: runtime, Spec: spec, Definition: definition, WorkingDirectory: root, Environment: environment, Sink: m.archive, RunID: m.runID,
		Progress: func(p subagent.ProgressEvent) {
			activity := agentActivity{Agent: p.Agent, TaskID: p.TaskID, ParentID: p.ParentID, Action: p.Action, Detail: p.Detail}
			emitAgentEvent(ctx, events, agentEvent{activity: &activity})
		},
		Trace: func(t subagent.TraceEvent) {
			trace := agentTrace{Agent: t.Agent, TaskID: t.TaskID, ParentID: t.ParentID, Kind: t.Kind, CallID: t.CallID, Name: t.Name, Content: t.Content, IsError: t.IsError}
			emitAgentEvent(ctx, events, agentEvent{trace: &trace})
		}}
	result, err := runner.Run(ctx, input)
	if err != nil {
		fail(err)
		return
	}
	emitAgentEvent(ctx, events, agentEvent{response: &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, Content: subagent.RenderTaskResult(result)}}}}})
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
	definition, err := m.resolvePublicAgent(name)
	if err != nil {
		m.status = err.Error()
		return m, nil
	}
	if definition.Info.Kind == subagent.AgentKindExternal {
		switch definition.Info.Name {
		case subagent.BuiltinWebSearchID:
			return m.startAgentSearch(input)
		case subagent.BuiltinWebTesterID:
			return m.startAgentWebTester(input)
		}
	}
	m.planArmed = false
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
	definition, err := a.state.resolvePublicAgent(name)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	if definition.Info.Kind == subagent.AgentKindExternal {
		switch definition.Info.Name {
		case subagent.BuiltinWebSearchID:
			return a.runACPAgentSearch(ctx, input)
		case subagent.BuiltinWebTesterID:
			return a.runACPAgentWebTester(ctx, input)
		}
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
	a.publishUsageUpdate()
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

// RunSubagents opens subagent profiles, external bindings, and ACP connection
// management without starting the chat runtime.
func RunSubagents(ctx context.Context, store config.Store, workspaceStore workspace.Store) error {
	value, err := store.Load()
	if err != nil {
		return err
	}
	m := newModel(ctx, store, nil)
	m.config = value
	m.workspaceStore = &workspaceStore
	updated, _ := m.enterCustom()
	m = updated.(model)
	if m.screen != screenCustom {
		return fmt.Errorf("open subagents: %s", m.status)
	}
	_, err = runStandalone(m, screenCustom)
	return err
}

func RunSubagentsDefault(ctx context.Context) error {
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	workspaceStore, err := workspace.DefaultStore()
	if err != nil {
		return err
	}
	if err := RunSubagents(ctx, store, workspaceStore); err != nil {
		return fmt.Errorf("q subagents: %w", err)
	}
	return nil
}
