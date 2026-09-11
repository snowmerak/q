package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/subagent"
	qtools "github.com/snowmerak/q/tools"
)

type loomResultCapturer interface {
	CaptureResult(context.Context, qtools.CaptureSource, client.ToolCall, client.ToolResult) (client.ToolResult, error)
}

type agentInvocationToolRuntime struct {
	base       agentToolRuntime
	invocation *subagent.InvocationRuntime
}

type reservedInvocationToolRuntime struct {
	base     agentToolRuntime
	preserve map[string]bool
}

func (r *reservedInvocationToolRuntime) Tools() []client.Tool {
	var result []client.Tool
	for _, tool := range r.base.Tools() {
		name := tool.Function.Name
		if (name == subagent.ExternalSearchToolName || name == subagent.ExternalWebTesterToolName) && !r.preserve[name] {
			continue
		}
		result = append(result, tool)
	}
	return result
}

func (r *reservedInvocationToolRuntime) Environment() qtools.HostEnvironment {
	return r.base.Environment()
}

func (r *reservedInvocationToolRuntime) Call(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
	if !toolAvailable(r, call.Function.Name) {
		return client.ToolResult{}, fmt.Errorf("tool %q is not available", call.Function.Name)
	}
	return r.base.Call(ctx, call)
}

func (r *agentInvocationToolRuntime) Tools() []client.Tool {
	return r.invocation.Tools()
}

func (r *agentInvocationToolRuntime) Environment() qtools.HostEnvironment {
	return r.base.Environment()
}

func (r *agentInvocationToolRuntime) Call(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
	return r.invocation.Call(ctx, call)
}

func configuredAgentToolRuntime(
	base agentToolRuntime,
	role string,
	value config.Config,
	root string,
) (agentToolRuntime, error) {
	scoped := scopeTools(base, role)
	if scoped == nil {
		return nil, nil
	}
	_, _, searchConfigured := value.ExternalAgentConnection(config.AgentRoleSearch)
	_, _, webTesterConfigured := value.ExternalAgentConnection(config.AgentRoleExternalWebTester)
	searchAuthorized := searchConfigured && (role == mcpconfig.RoleDefault || role == config.AgentRoleGriller || role == config.AgentRolePlanner || role == config.AgentRoleAdvisor)
	webTesterAuthorized := webTesterConfigured && role == mcpconfig.RoleDefault
	_, alreadyConfigured := base.(*agentInvocationToolRuntime)
	preserve := map[string]bool{
		subagent.ExternalSearchToolName:    alreadyConfigured && searchAuthorized && toolAvailable(base, subagent.ExternalSearchToolName),
		subagent.ExternalWebTesterToolName: alreadyConfigured && webTesterAuthorized && toolAvailable(base, subagent.ExternalWebTesterToolName),
	}
	scoped = &reservedInvocationToolRuntime{base: scoped, preserve: preserve}
	var invocations []subagent.Invocation
	if searchAuthorized && !preserve[subagent.ExternalSearchToolName] {
		if search, configured := configuredExternalSearchInvocation(value, root); configured {
			invocations = append(invocations, search)
		}
	}
	if webTesterAuthorized && !preserve[subagent.ExternalWebTesterToolName] {
		if webTester, configured := configuredExternalWebTesterInvocation(value, root); configured {
			invocations = append(invocations, webTester)
		}
	}
	if len(invocations) == 0 {
		return scoped, nil
	}
	capture := configuredInvocationCapture(base)
	if capture == nil {
		return nil, errors.New("agent invocation requires Loom capture")
	}
	runtime, err := subagent.NewInvocationRuntime(scoped, capture, invocations...)
	if err != nil {
		return nil, err
	}
	return &agentInvocationToolRuntime{base: scoped, invocation: runtime}, nil
}

func configuredInvocationCapture(base agentToolRuntime) subagent.InvocationCaptureFunc {
	capturer, ok := base.(loomResultCapturer)
	if !ok {
		return nil
	}
	return func(
		ctx context.Context,
		source subagent.InvocationSource,
		call client.ToolCall,
		result client.ToolResult,
	) (client.ToolResult, error) {
		captured, err := capturer.CaptureResult(ctx, qtools.CaptureSource{
			Protocol: source.Protocol, Name: source.Name, Kind: source.Kind, MediaType: source.MediaType,
		}, call, result)
		if err != nil {
			return client.ToolResult{}, fmt.Errorf("capture agent invocation %s: %w", call.Function.Name, err)
		}
		return captured, nil
	}
}
