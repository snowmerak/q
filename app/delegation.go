package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
)

const (
	maximumDelegationDepth = 4
	maximumDelegationCalls = 32
)

type delegationDispatcher struct {
	registry         *subagent.Registry
	client           chatClient
	tools            agentToolRuntime
	value            config.Config
	workingDirectory string
	environment      string
	runID            string
	sink             subagent.RecordSink
	capture          subagent.InvocationCaptureFunc
	external         map[string]subagent.Invocation
	store            *workspace.Store
	apiMode          func(string) string
	progress         subagent.ProgressFunc
	trace            subagent.TraceFunc

	mu       sync.Mutex
	calls    int
	models   []client.Model
	modelErr error
	loaded   bool
}

type delegationRuntime struct {
	base           agentToolRuntime
	dispatcher     *delegationDispatcher
	caller         string
	stack          []string
	store          *workspace.Store
	taskID         string
	delegatedTools map[string]bool
}

type delegateInput struct {
	SubagentName string `json:"subagent_name"`
	Prompt       string `json:"prompt"`
}

// observeDelegation forwards the entire nested runner tree through one event
// stream. The dispatcher is shared by descendants, preserving task lineage.
func (r *delegationRuntime) observeDelegation(ctx context.Context, events chan<- agentEvent) {
	r.observeDelegationEvents(func(event agentEvent) { _ = emitAgentEvent(ctx, events, event) })
}

func (r *delegationRuntime) observeDelegationEvents(emit func(agentEvent)) {
	if r == nil || r.dispatcher == nil {
		return
	}
	r.dispatcher.progress = func(p subagent.ProgressEvent) {
		activity := agentActivity{Agent: p.Agent, TaskID: p.TaskID, ParentID: p.ParentID, Action: p.Action, Detail: p.Detail}
		emit(agentEvent{activity: &activity})
	}
	r.dispatcher.trace = func(t subagent.TraceEvent) {
		trace := agentTrace{Agent: t.Agent, TaskID: t.TaskID, ParentID: t.ParentID, CallID: t.CallID, Kind: t.Kind, Name: t.Name, Content: t.Content, IsError: t.IsError}
		emit(agentEvent{trace: &trace})
	}
}

func buildSubagentRegistry(store subagent.ProfileStore) (*subagent.Registry, error) {
	definitions := subagent.PublicAgentDefinitions()
	for _, entry := range store.List() {
		if entry.Err != nil {
			continue
		}
		definition, err := subagent.DefinitionForProfile(entry)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, definition)
	}
	return subagent.NewRegistry(definitions)
}

func (m model) configuredDelegationRuntime(base agentToolRuntime, root string) (agentToolRuntime, error) {
	return m.configuredDelegationRuntimeFor(base, root, "", nil)
}

func (m model) configuredDelegationRuntimeFor(base agentToolRuntime, root, caller string, stack []string) (agentToolRuntime, error) {
	if base == nil || m.client == nil {
		return base, nil
	}
	registry, err := buildSubagentRegistry(m.customStore())
	if err != nil {
		return nil, err
	}
	catalog := m.customTools()
	if catalog == nil {
		catalog = base
	}
	environment := fmt.Sprintf("%+v", base.Environment())
	dispatcher := &delegationDispatcher{
		registry: registry, client: m.client, tools: catalog, value: m.activeConfig(),
		workingDirectory: root, environment: environment, runID: m.runID,
		capture: configuredInvocationCapture(m.toolRuntime), external: configuredExternalDelegates(m.activeConfig(), root, registry),
		apiMode: m.activeModelAPIMode,
	}
	if m.runID != "" {
		dispatcher.store = m.workspaceStore
		if dispatcher.store != nil {
			count, err := countSavedDelegations(*dispatcher.store, m.runID, 0)
			if err != nil {
				return nil, err
			}
			dispatcher.calls = count
		}
	}
	if strings.TrimSpace(m.runID) != "" {
		dispatcher.sink = m.archive
	}
	runtime := &delegationRuntime{base: base, dispatcher: dispatcher, caller: caller, stack: append([]string(nil), stack...), store: dispatcher.store}
	if caller == "" && m.loopMode == loopModeDelegation {
		runtime.delegatedTools = make(map[string]bool)
		for _, definition := range subagent.PublicAgentDefinitions() {
			if definition.Info.Source != "builtin" || !dispatcher.available(definition.Info.Name) {
				continue
			}
			for _, name := range definition.Tools {
				runtime.delegatedTools[name] = true
			}
			switch definition.Info.Name {
			case subagent.BuiltinWebSearchID:
				runtime.delegatedTools[subagent.ExternalSearchToolName] = true
			case subagent.BuiltinWebTesterID:
				runtime.delegatedTools[subagent.ExternalWebTesterToolName] = true
			}
		}
	}
	return runtime, nil
}

func countSavedDelegations(store workspace.Store, runID string, depth int) (int, error) {
	if depth > maximumDelegationDepth {
		return 0, errors.New("saved delegation tree exceeds depth limit")
	}
	items, err := store.LoadDelegations()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, item := range items {
		if item.RunID != runID {
			continue
		}
		count++
		child, err := store.ChildStore(item.InvocationID)
		if err != nil {
			return 0, err
		}
		nested, err := countSavedDelegations(child, runID, depth+1)
		if err != nil {
			return 0, err
		}
		count += nested
	}
	return count, nil
}

func (r *delegationRuntime) Tools() []client.Tool {
	if r == nil {
		return nil
	}
	result := make([]client.Tool, 0, len(r.base.Tools())+2)
	for _, tool := range r.base.Tools() {
		if !r.delegatedTools[tool.Function.Name] {
			result = append(result, tool)
		}
	}
	if len(r.available()) > 0 {
		result = append(result, subagent.DelegateTools()...)
	}
	return result
}

func (r *delegationRuntime) Environment() qtools.HostEnvironment {
	if r == nil || r.base == nil {
		return qtools.HostEnvironment{}
	}
	return r.base.Environment()
}

func (r *delegationRuntime) Call(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
	if r == nil || r.dispatcher == nil {
		return client.ToolResult{}, errors.New("subagent delegation is unavailable")
	}
	switch call.Function.Name {
	case subagent.DelegateListToolName:
		var input map[string]any
		if err := decodeDelegationArguments(call.Function.Arguments, &input); err != nil || len(input) != 0 {
			if err == nil {
				err = errors.New("delegate_list accepts no arguments")
			}
			return client.ToolResult{Content: err.Error(), IsError: true}, nil
		}
		body, err := json.Marshal(r.available())
		if err != nil {
			return client.ToolResult{}, err
		}
		return client.ToolResult{Content: string(body)}, nil
	case subagent.DelegateToolName:
		var input delegateInput
		if err := decodeDelegationArguments(call.Function.Arguments, &input); err != nil {
			return client.ToolResult{Content: err.Error(), IsError: true}, nil
		}
		input.SubagentName = strings.TrimSpace(input.SubagentName)
		input.Prompt = strings.TrimSpace(input.Prompt)
		if input.SubagentName == "" || input.Prompt == "" {
			return client.ToolResult{Content: "subagent_name and prompt are required", IsError: true}, nil
		}
		if len(input.Prompt) > subagent.MaximumDelegatePromptBytes {
			return client.ToolResult{Content: fmt.Sprintf("prompt must not exceed %d bytes", subagent.MaximumDelegatePromptBytes), IsError: true}, nil
		}
		return r.dispatcher.dispatch(ctx, r.caller, r.stack, r.store, r.taskID, call, input)
	default:
		if r.delegatedTools[call.Function.Name] {
			return client.ToolResult{Content: fmt.Sprintf("%s is delegated in this mode; use delegate", call.Function.Name), IsError: true}, nil
		}
		return r.base.Call(ctx, call)
	}
}

func (r *delegationRuntime) SearchSkillHints(ctx context.Context, query string, limit int) (qtools.SkillHintSearchResult, error) {
	searcher, ok := r.base.(skillHintSearcher)
	if !ok {
		return qtools.SkillHintSearchResult{}, errors.New("agent skills hint search is unavailable")
	}
	return searcher.SearchSkillHints(ctx, query, limit)
}

func (r *delegationRuntime) available() []subagent.DelegateInfo {
	if r == nil || r.dispatcher == nil {
		return nil
	}
	allowed := r.dispatcher.registry.Allowed(r.caller)
	result := allowed[:0]
	for _, info := range allowed {
		if containsAgentName(r.stack, info.Name) || !r.dispatcher.available(info.Name) {
			continue
		}
		result = append(result, info)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (d *delegationDispatcher) available(name string) bool {
	definition, found := d.registry.Get(name)
	if !found {
		return false
	}
	if definition.Info.Kind == subagent.AgentKindExternal {
		_, configured := d.external[name]
		return configured && d.capture != nil
	}
	if definition.Info.Kind != subagent.AgentKindInner || !d.value.HasNativeRole(definition.Info.Role) {
		return false
	}
	if !definition.StrictTools {
		return true
	}
	available := make(map[string]struct{})
	for _, tool := range d.tools.Tools() {
		available[tool.Function.Name] = struct{}{}
	}
	for _, name := range definition.Tools {
		if _, found := available[name]; !found {
			return false
		}
	}
	return true
}

func (d *delegationDispatcher) dispatch(
	ctx context.Context,
	caller string,
	stack []string,
	parentStore *workspace.Store,
	parentTaskID string,
	call client.ToolCall,
	input delegateInput,
) (client.ToolResult, error) {
	if len(stack) >= maximumDelegationDepth {
		return client.ToolResult{Content: fmt.Sprintf("delegation depth exceeds %d", maximumDelegationDepth), IsError: true}, nil
	}
	if containsAgentName(stack, input.SubagentName) {
		return client.ToolResult{Content: "delegation cycle detected", IsError: true}, nil
	}
	if parentStore != nil {
		if call.ID == "" {
			return client.ToolResult{}, errors.New("persisted delegation requires a tool call ID")
		}
		return d.dispatchStored(ctx, caller, stack, *parentStore, parentTaskID, call, input)
	}
	if !d.registry.CanDelegate(caller, input.SubagentName) || !d.available(input.SubagentName) {
		return client.ToolResult{Content: fmt.Sprintf("subagent %q is not allowed or available", input.SubagentName), IsError: true}, nil
	}
	d.mu.Lock()
	if d.calls >= maximumDelegationCalls {
		d.mu.Unlock()
		return client.ToolResult{Content: fmt.Sprintf("delegation call limit %d reached", maximumDelegationCalls), IsError: true}, nil
	}
	d.calls++
	d.mu.Unlock()

	definition, _ := d.registry.Get(input.SubagentName)
	if definition.Info.Kind == subagent.AgentKindExternal {
		return d.dispatchExternalObserved(ctx, definition, call, input.Prompt, call.ID, "")
	}
	models, err := d.loadModels(ctx)
	if err != nil {
		return client.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	spec, err := subagent.Resolve(d.value, definition.Info.Role, models)
	if err != nil {
		return client.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	childStack := append(append([]string(nil), stack...), input.SubagentName)
	runtime := &delegationRuntime{base: d.tools, dispatcher: d, caller: input.SubagentName, stack: childStack}
	result, err := (subagent.GeneralRunner{
		Client: d.client, Tools: runtime, Spec: spec, Definition: definition,
		WorkingDirectory: d.workingDirectory, Environment: d.environment, RunID: d.runID,
		Sink: d.sink, Progress: d.progress, Trace: d.trace,
	}).Run(ctx, input.Prompt)
	if err != nil {
		return client.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return client.ToolResult{}, err
	}
	raw := client.ToolResult{Content: string(body)}
	if d.capture == nil {
		return raw, nil
	}
	return d.capture(ctx, subagent.InvocationSource{
		Protocol: "q-subagent", Name: input.SubagentName, Kind: "agent-result",
		MediaType: "application/vnd.q.agent-result+json",
	}, call, raw)
}

func pendingDelegationPosition(messages []client.Message, call client.ToolCall) (int, int, error) {
	for messageIndex := len(messages) - 1; messageIndex >= 0; messageIndex-- {
		message := messages[messageIndex]
		if message.Role != client.RoleAssistant || len(message.ToolCalls) == 0 {
			continue
		}
		completed := 0
		for _, following := range messages[messageIndex+1:] {
			if following.Role != client.RoleTool {
				completed = len(message.ToolCalls)
				break
			}
			completed++
		}
		for toolIndex := completed; toolIndex < len(message.ToolCalls); toolIndex++ {
			candidate := message.ToolCalls[toolIndex]
			if candidate.ID == call.ID && candidate.Function.Name == call.Function.Name && candidate.Function.Arguments == call.Function.Arguments {
				return messageIndex, toolIndex, nil
			}
		}
	}
	return 0, 0, errors.New("delegate call was not saved in the parent session")
}

func (d *delegationDispatcher) dispatchStored(ctx context.Context, caller string, stack []string, parent workspace.Store, parentTaskID string, call client.ToolCall, input delegateInput) (client.ToolResult, error) {
	parentSession, err := parent.Load()
	if err != nil {
		return client.ToolResult{}, err
	}
	callIndex, toolIndex, err := pendingDelegationPosition(parentSession.Transcript, call)
	if err != nil {
		return client.ToolResult{}, err
	}
	items, err := parent.LoadDelegations()
	if err != nil {
		return client.ToolResult{}, err
	}
	var bookmark workspace.DelegationBookmark
	for _, item := range items {
		if item.CallIndex == callIndex && item.ToolIndex == toolIndex {
			bookmark = item
			break
		}
	}
	allowed := d.registry.CanDelegate(caller, input.SubagentName) && d.available(input.SubagentName)
	if bookmark.InvocationID == "" && !allowed {
		return client.ToolResult{Content: fmt.Sprintf("subagent %q is not allowed or available", input.SubagentName), IsError: true}, nil
	}
	if bookmark.InvocationID == "" {
		id, idErr := workspace.NewSessionID()
		if idErr != nil {
			return client.ToolResult{}, idErr
		}
		bookmark = workspace.DelegationBookmark{
			InvocationID: id, CallIndex: callIndex, ToolIndex: toolIndex,
			CallID: call.ID, Agent: input.SubagentName, Prompt: input.Prompt,
			RunID: parentSession.RunID, CreatedAt: time.Now().UTC(),
		}
		d.mu.Lock()
		if d.calls >= maximumDelegationCalls {
			d.mu.Unlock()
			return client.ToolResult{Content: fmt.Sprintf("delegation call limit %d reached", maximumDelegationCalls), IsError: true}, nil
		}
		bookmarked, addErr := parent.AddDelegation(bookmark)
		if addErr != nil {
			d.mu.Unlock()
			return client.ToolResult{}, addErr
		}
		bookmark = bookmarked
		if bookmark.InvocationID == id {
			d.calls++
		}
		d.mu.Unlock()
	} else {
		bookmark, err = parent.AddDelegation(bookmark)
		if err != nil {
			return client.ToolResult{}, err
		}
	}
	if bookmark.RunID != parentSession.RunID || bookmark.Agent != input.SubagentName || bookmark.Prompt != input.Prompt {
		return client.ToolResult{}, errors.New("delegation bookmark does not match parent call")
	}
	child, err := parent.ChildStore(bookmark.InvocationID)
	if err != nil {
		return client.ToolResult{}, err
	}
	state, stateErr := child.LoadDelegationState()
	if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
		return client.ToolResult{}, stateErr
	}
	if stateErr == nil {
		if state.Agent != bookmark.Agent || state.Prompt != bookmark.Prompt || state.RunID != bookmark.RunID {
			return client.ToolResult{}, errors.New("delegation state does not match bookmark")
		}
		if state.Result != nil {
			return *state.Result, nil
		}
	}
	if !allowed {
		blocked := client.ToolResult{Content: fmt.Sprintf("saved subagent %q is no longer allowed or available", input.SubagentName), IsError: true}
		if stateErr != nil {
			state = workspace.DelegationState{Agent: bookmark.Agent, Prompt: bookmark.Prompt, RunID: bookmark.RunID, ParentID: parentTaskID, TaskID: bookmark.InvocationID}
		}
		state.Status, state.Result = "blocked", &blocked
		if err := child.SaveDelegationState(state); err != nil {
			return client.ToolResult{}, err
		}
		return blocked, nil
	}
	definition, _ := d.registry.Get(input.SubagentName)
	if definition.Info.Kind == subagent.AgentKindExternal {
		if stateErr == nil {
			unknown := client.ToolResult{Content: `{"status":"unknown","detail":"external agent outcome could not be confirmed after session restart"}`, IsError: true}
			state.Status, state.Result = "unknown", &unknown
			if err := child.SaveDelegationState(state); err != nil {
				return client.ToolResult{}, err
			}
			return unknown, nil
		}
		if err := child.Save(workspace.Session{RunID: bookmark.RunID, Transcript: []client.Message{{Role: client.RoleUser, Content: input.Prompt}}, Context: []client.Message{{Role: client.RoleUser, Content: input.Prompt}}}); err != nil {
			return client.ToolResult{}, err
		}
		state = workspace.DelegationState{Agent: bookmark.Agent, Prompt: bookmark.Prompt, RunID: bookmark.RunID, ParentID: parentTaskID, TaskID: bookmark.InvocationID, Status: "running"}
		if err := child.SaveDelegationState(state); err != nil {
			return client.ToolResult{}, err
		}
		result, callErr := d.dispatchExternalObserved(ctx, definition, call, input.Prompt, state.TaskID, parentTaskID)
		if callErr != nil {
			return client.ToolResult{}, callErr
		}
		state.Status, state.Result = "completed", &result
		if err := child.SaveDelegationState(state); err != nil {
			return client.ToolResult{}, err
		}
		return result, nil
	}
	models, err := d.loadModels(ctx)
	if err != nil {
		return client.ToolResult{}, err
	}
	spec, err := subagent.Resolve(d.value, definition.Info.Role, models)
	if err != nil {
		if stateErr == nil {
			blocked := client.ToolResult{Content: "saved delegated model is unavailable: " + err.Error(), IsError: true}
			state.Status, state.Result = "blocked", &blocked
			if saveErr := child.SaveDelegationState(state); saveErr != nil {
				return client.ToolResult{}, saveErr
			}
			return blocked, nil
		}
		return client.ToolResult{}, err
	}
	var resume *subagent.GeneralRunState
	if stateErr == nil {
		childSession, loadErr := child.Load()
		if errors.Is(loadErr, workspace.ErrNotFound) && state.Round == 0 {
			childSession = workspace.Session{RunID: bookmark.RunID, Transcript: []client.Message{{Role: client.RoleUser, Content: input.Prompt}}, Context: []client.Message{{Role: client.RoleUser, Content: input.Prompt}}}
			loadErr = child.Save(childSession)
		} else if errors.Is(loadErr, workspace.ErrNotFound) {
			blocked := client.ToolResult{Content: "saved delegated conversation is missing", IsError: true}
			state.Status, state.Result = "blocked", &blocked
			if err := child.SaveDelegationState(state); err != nil {
				return client.ToolResult{}, err
			}
			return blocked, nil
		}
		if loadErr != nil {
			return client.ToolResult{}, loadErr
		}
		if len(childSession.Transcript) >= 2 {
			updated, changed, reconcileErr := reconcileChildExecutionState(childSession, state, spec)
			if reconcileErr != nil {
				blocked := client.ToolResult{Content: "saved delegated execution state is inconsistent: " + reconcileErr.Error(), IsError: true}
				state.Status, state.Result = "blocked", &blocked
				if err := child.SaveDelegationState(state); err != nil {
					return client.ToolResult{}, err
				}
				return blocked, nil
			}
			if changed {
				state = updated
				if err := child.SaveDelegationState(state); err != nil {
					return client.ToolResult{}, err
				}
			}
		}
		mode := ""
		if d.apiMode != nil {
			mode = d.apiMode(state.Model)
		}
		if state.APIMode != mode {
			blocked := client.ToolResult{Content: "saved delegated model API mode is unavailable", IsError: true}
			state.Status, state.Result = "blocked", &blocked
			if err := child.SaveDelegationState(state); err != nil {
				return client.ToolResult{}, err
			}
			return blocked, nil
		}
		if len(childSession.Transcript) >= 2 {
			resume = &subagent.GeneralRunState{
				Transcript: childSession.Transcript, Context: restoreResponseReplay(childSession.Context, childSession.ResponseReplay),
				Round: state.Round, Started: state.Started, Reminders: state.Reminders,
				Spec: subagent.SpecCheckpoint{Model: state.Model, Candidate: state.Candidate, ConversationID: state.ConversationID},
			}
		} else if state.Round != 0 {
			blocked := client.ToolResult{Content: "saved delegated conversation is incomplete", IsError: true}
			state.Status, state.Result = "blocked", &blocked
			if err := child.SaveDelegationState(state); err != nil {
				return client.ToolResult{}, err
			}
			return blocked, nil
		}
	} else {
		if err := child.Save(workspace.Session{RunID: bookmark.RunID, Transcript: []client.Message{{Role: client.RoleUser, Content: input.Prompt}}, Context: []client.Message{{Role: client.RoleUser, Content: input.Prompt}}}); err != nil {
			return client.ToolResult{}, err
		}
		mode := ""
		if d.apiMode != nil {
			mode = d.apiMode(spec.Model)
		}
		state = workspace.DelegationState{Agent: bookmark.Agent, Prompt: bookmark.Prompt, RunID: bookmark.RunID, ParentID: parentTaskID, TaskID: bookmark.InvocationID, Model: spec.Model, APIMode: mode, Status: "running"}
		if err := child.SaveDelegationState(state); err != nil {
			return client.ToolResult{}, err
		}
	}
	if resume != nil {
		if err := spec.Restore(resume.Spec); err != nil {
			blocked := client.ToolResult{Content: err.Error(), IsError: true}
			state.Status, state.Result = "blocked", &blocked
			if saveErr := child.SaveDelegationState(state); saveErr != nil {
				return client.ToolResult{}, saveErr
			}
			return blocked, nil
		}
	}
	childStack := append(append([]string(nil), stack...), input.SubagentName)
	runtime := &delegationRuntime{base: d.tools, dispatcher: d, caller: input.SubagentName, stack: childStack, store: &child, taskID: state.TaskID}
	checkpoint := func(saved subagent.GeneralRunState) error {
		mode := ""
		if d.apiMode != nil {
			mode = d.apiMode(saved.Spec.Model)
		}
		if err := child.Save(workspace.Session{RunID: bookmark.RunID, Transcript: saved.Transcript, Context: saved.Context, ResponseReplay: collectResponseReplay(saved.Context), ResponseAffinity: &workspace.ResponseAffinity{Model: saved.Spec.Model, Key: saved.Spec.ConversationID, APIMode: mode, Candidate: saved.Spec.Candidate}}); err != nil {
			return err
		}
		state.Round, state.Started, state.Reminders = saved.Round, saved.Started, saved.Reminders
		state.Model, state.Candidate, state.ConversationID = saved.Spec.Model, saved.Spec.Candidate, saved.Spec.ConversationID
		state.RunningCall, state.UnknownTools = delegatedToolOutcomes(saved.Transcript)
		state.APIMode = mode
		return child.SaveDelegationState(state)
	}
	result, runErr := (subagent.GeneralRunner{
		Client: d.client, Tools: runtime, Spec: spec, Definition: definition,
		WorkingDirectory: d.workingDirectory, Environment: d.environment, RunID: d.runID,
		ParentID: parentTaskID, TaskID: state.TaskID, Sink: d.sink, Progress: d.progress, Trace: d.trace, Resume: resume, Checkpoint: checkpoint,
	}).Run(ctx, input.Prompt)
	if runErr != nil {
		return client.ToolResult{}, runErr
	}
	body, err := json.Marshal(result)
	if err != nil {
		return client.ToolResult{}, err
	}
	raw := client.ToolResult{Content: string(body)}
	if d.capture != nil {
		raw, err = d.capture(ctx, subagent.InvocationSource{Protocol: "q-subagent", Name: input.SubagentName, Kind: "agent-result", MediaType: "application/vnd.q.agent-result+json"}, call, raw)
		if err != nil {
			return client.ToolResult{}, err
		}
	}
	state.Status, state.Result = "completed", &raw
	if err := child.SaveDelegationState(state); err != nil {
		return client.ToolResult{}, err
	}
	return raw, nil
}

func delegatedToolOutcomes(messages []client.Message) (*workspace.DelegationToolCall, []workspace.DelegationToolCall) {
	var running *workspace.DelegationToolCall
	var unknown []workspace.DelegationToolCall
	for messageIndex, message := range messages {
		if message.Role != client.RoleAssistant || len(message.ToolCalls) == 0 {
			continue
		}
		for toolIndex, call := range message.ToolCalls {
			position := workspace.DelegationToolCall{MessageIndex: messageIndex, ToolIndex: toolIndex, CallID: call.ID, Name: call.Function.Name}
			resultIndex := messageIndex + 1 + toolIndex
			if resultIndex >= len(messages) || messages[resultIndex].Role != client.RoleTool {
				running = &position
				continue
			}
			if strings.Contains(messages[resultIndex].Content, `"status":"unknown"`) {
				unknown = append(unknown, position)
			}
		}
	}
	return running, unknown
}

// The child session is written before its execution marker. Repair the marker
// from the newer transcript after a crash between those two file replacements.
func reconcileChildExecutionState(session workspace.Session, state workspace.DelegationState, spec subagent.Spec) (workspace.DelegationState, bool, error) {
	changed := false
	rounds, reminders := 0, 0
	started := false
	for index, message := range session.Transcript {
		switch message.Role {
		case client.RoleAssistant:
			if index > 0 && session.Transcript[index-1].Role == client.RoleTool && session.Transcript[index-1].Name == subagent.TaskCompleteToolName {
				continue
			}
			rounds++
		case client.RoleSystem:
			if index > 1 {
				reminders++
			}
		case client.RoleTool:
			if message.Name == subagent.TaskStartToolName {
				var result struct {
					Started bool `json:"started"`
				}
				if json.Unmarshal([]byte(message.Content), &result) == nil && result.Started {
					started = true
				}
			}
		}
	}
	if state.Round > rounds || state.Reminders > reminders || (state.Started && !started) {
		return state, false, errors.New("delegation state is ahead of its child transcript")
	}
	if state.Round != rounds {
		state.Round, changed = rounds, true
	}
	if state.Reminders != reminders {
		state.Reminders, changed = reminders, true
	}
	if started && !state.Started {
		state.Started, changed = true, true
	}
	if affinity := session.ResponseAffinity; affinity != nil && affinity.Model != "" {
		candidate := -1
		if affinity.Candidate >= 0 && affinity.Candidate < len(spec.Candidates) && spec.Candidates[affinity.Candidate].Model == affinity.Model {
			candidate = affinity.Candidate
		}
		if candidate < 0 {
			for index, value := range spec.Candidates {
				if value.Model == affinity.Model {
					candidate = index
					break
				}
			}
		}
		if candidate < 0 {
			return state, false, fmt.Errorf("saved delegated model %q is unavailable", affinity.Model)
		}
		if state.Model != affinity.Model || state.Candidate != candidate || state.ConversationID != affinity.Key {
			state.Model, state.Candidate, state.ConversationID, changed = affinity.Model, candidate, affinity.Key, true
		}
		if affinity.APIMode != "" && state.APIMode != affinity.APIMode {
			state.APIMode, changed = affinity.APIMode, true
		}
	} else {
		for index := len(session.Transcript) - 1; index >= 0; index-- {
			message := session.Transcript[index]
			if message.Role == client.RoleAssistant && message.ResponseModel != "" && message.ResponseModel != state.Model {
				return state, false, errors.New("saved delegated model differs from child transcript")
			}
			if message.Role == client.RoleAssistant {
				break
			}
		}
	}
	running, unknown := delegatedToolOutcomes(session.Transcript)
	if !reflect.DeepEqual(state.RunningCall, running) || !reflect.DeepEqual(state.UnknownTools, unknown) {
		state.RunningCall, state.UnknownTools, changed = running, unknown, true
	}
	return state, changed, nil
}

func configuredExternalDelegates(value config.Config, root string, registry *subagent.Registry) map[string]subagent.Invocation {
	result := make(map[string]subagent.Invocation)
	for _, info := range registry.List() {
		if info.Kind != subagent.AgentKindExternal {
			continue
		}
		definition, _ := registry.Get(info.Name)
		connectionID, connection, configured := externalDefinitionConnection(value, definition)
		if !configured {
			continue
		}
		switch info.Name {
		case subagent.BuiltinWebSearchID:
			if invocation, available := configuredExternalSearchInvocation(value, root); available {
				result[info.Name] = invocation
			}
		case subagent.BuiltinWebTesterID:
			if invocation, available := configuredExternalWebTesterInvocation(value, root); available {
				result[info.Name] = invocation
			}
		default:
			result[info.Name] = configuredExternalSubagentInvocation(root, connectionID, connection, definition)
		}
	}
	return result
}

func externalDefinitionConnection(value config.Config, definition subagent.AgentDefinition) (string, config.AgentConnectionConfig, bool) {
	if definition.Info.Kind != subagent.AgentKindExternal {
		return "", config.AgentConnectionConfig{}, false
	}
	if definition.Connection == "" {
		return value.ExternalAgentConnection(definition.Info.Role)
	}
	connection, found := value.Agents.Connections[definition.Connection]
	if !found || connection.Disabled {
		return "", config.AgentConnectionConfig{}, false
	}
	return definition.Connection, connection, true
}

func (d *delegationDispatcher) dispatchExternal(
	ctx context.Context,
	definition subagent.AgentDefinition,
	parentCall client.ToolCall,
	prompt string,
) (client.ToolResult, error) {
	invocation, configured := d.external[definition.Info.Name]
	if !configured || d.capture == nil {
		return client.ToolResult{Content: fmt.Sprintf("subagent %q is unavailable", definition.Info.Name), IsError: true}, nil
	}
	var input any
	switch definition.Info.Name {
	case subagent.BuiltinWebSearchID:
		input = subagent.ExternalSearchInput{Query: prompt}
	case subagent.BuiltinWebTesterID:
		input = subagent.ExternalWebTesterInput{Request: prompt}
	default:
		input = externalSubagentInput{Request: prompt}
	}
	arguments, err := json.Marshal(input)
	if err != nil {
		return client.ToolResult{}, fmt.Errorf("encode %s delegation: %w", definition.Info.Name, err)
	}
	externalCall := client.ToolCall{
		ID: parentCall.ID, Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: invocation.Tool.Function.Name, Arguments: string(arguments)},
	}
	runtime, err := subagent.NewInvocationRuntime(nil, d.capture, invocation)
	if err != nil {
		return client.ToolResult{}, err
	}
	return runtime.Call(ctx, externalCall)
}

func (d *delegationDispatcher) dispatchExternalObserved(ctx context.Context, definition subagent.AgentDefinition, call client.ToolCall, prompt, taskID, parentID string) (client.ToolResult, error) {
	report := func(action, detail string) {
		if d.progress != nil {
			d.progress(subagent.ProgressEvent{Agent: definition.Info.Name, TaskID: taskID, ParentID: parentID, Action: action, Detail: detail})
		}
	}
	report(subagent.ProgressStarted, "external agent")
	result, err := d.dispatchExternal(ctx, definition, call, prompt)
	if err != nil {
		report(subagent.ProgressFailed, err.Error())
	} else if result.IsError {
		report(subagent.ProgressFailed, result.Content)
	} else {
		report(subagent.ProgressCompleted, "external agent returned")
	}
	return result, err
}

func (d *delegationDispatcher) loadModels(ctx context.Context) ([]client.Model, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.loaded {
		d.models, d.modelErr = d.client.ListModels(ctx)
		d.loaded = true
	}
	return append([]client.Model(nil), d.models...), d.modelErr
}

func decodeDelegationArguments(arguments string, output any) error {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("invalid delegation arguments: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("invalid delegation arguments: multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid delegation arguments: %w", err)
	}
	return nil
}

func containsAgentName(values []string, target string) bool {
	return slices.Contains(values, target)
}
