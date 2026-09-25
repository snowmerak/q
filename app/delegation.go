package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	qtools "github.com/snowmerak/q/tools"
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

	mu       sync.Mutex
	calls    int
	models   []client.Model
	modelErr error
	loaded   bool
}

type delegationRuntime struct {
	base       agentToolRuntime
	dispatcher *delegationDispatcher
	caller     string
	stack      []string
}

type delegateInput struct {
	SubagentName string `json:"subagent_name"`
	Prompt       string `json:"prompt"`
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
	}
	if strings.TrimSpace(m.runID) != "" {
		dispatcher.sink = m.archive
	}
	return &delegationRuntime{base: base, dispatcher: dispatcher, caller: caller, stack: append([]string(nil), stack...)}, nil
}

func (r *delegationRuntime) Tools() []client.Tool {
	if r == nil {
		return nil
	}
	result := append([]client.Tool(nil), r.base.Tools()...)
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
		return r.dispatcher.dispatch(ctx, r.caller, r.stack, call, input)
	default:
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
	call client.ToolCall,
	input delegateInput,
) (client.ToolResult, error) {
	if !d.registry.CanDelegate(caller, input.SubagentName) || !d.available(input.SubagentName) {
		return client.ToolResult{Content: fmt.Sprintf("subagent %q is not allowed or available", input.SubagentName), IsError: true}, nil
	}
	if len(stack) >= maximumDelegationDepth {
		return client.ToolResult{Content: fmt.Sprintf("delegation depth exceeds %d", maximumDelegationDepth), IsError: true}, nil
	}
	if containsAgentName(stack, input.SubagentName) {
		return client.ToolResult{Content: "delegation cycle detected", IsError: true}, nil
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
		return d.dispatchExternal(ctx, definition, call, input.Prompt)
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
		Sink: d.sink,
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
