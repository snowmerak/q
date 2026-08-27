package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

type acpTraceKey struct {
	agent, taskID, parentID, callID, name string
}

type acpTraceCall struct {
	id       acp.ToolCallId
	sequence uint64
	trace    agentTrace
}

// acpPlanTrace projects model-visible subagent traffic to the client, not to
// the main agent's conversation. Model call IDs are scoped to each invocation
// and may be missing or reused; ACP IDs must be unique throughout the session.
type acpPlanTrace struct {
	root    string
	prefix  string
	nextID  uint64
	pending map[acpTraceKey][]acpTraceCall
	emit    func(context.Context, acp.SessionUpdate) error
}

func newACPPlanTrace(root, prefix string, emit func(context.Context, acp.SessionUpdate) error) *acpPlanTrace {
	return &acpPlanTrace{
		root: root, prefix: prefix, emit: emit,
		pending: make(map[acpTraceKey][]acpTraceCall),
	}
}

func (p *acpPlanTrace) handle(ctx context.Context, trace agentTrace) error {
	switch trace.Kind {
	case subagent.TraceAssistant:
		if strings.TrimSpace(trace.Content) == "" {
			return nil
		}
		return p.emit(ctx, acp.UpdateAgentThoughtText(acpTraceAgentLabel(trace.Agent, trace.TaskID)+" · "+trace.Content+"\n"))
	case subagent.TraceToolCall:
		return p.start(ctx, trace)
	case subagent.TraceToolResult:
		key := traceKey(trace)
		if len(p.pending[key]) == 0 {
			// A partial trace must still produce a valid ACP lifecycle, without
			// attaching the result to another task's call of the same tool.
			start := trace
			start.Kind, start.Content, start.IsError = subagent.TraceToolCall, "", false
			if err := p.start(ctx, start); err != nil {
				return err
			}
		}
		return p.finish(ctx, key, trace.Content, trace.IsError)
	}
	return nil
}

func (p *acpPlanTrace) start(ctx context.Context, trace agentTrace) error {
	p.nextID++
	call := acpTraceCall{
		id:       acp.ToolCallId(fmt.Sprintf("q-subagent-%s-%d", p.prefix, p.nextID)),
		sequence: p.nextID, trace: trace,
	}
	tool := client.ToolCall{ID: trace.CallID, Type: client.ToolTypeFunction, Function: client.FunctionCall{
		Name: trace.Name, Arguments: trace.Content,
	}}
	update := acp.StartToolCall(call.id, acpTraceAgentLabel(trace.Agent, trace.TaskID)+" · "+describeToolCall(tool),
		acp.WithStartKind(classifyACPTool(trace.Name)),
		acp.WithStartStatus(acp.ToolCallStatusInProgress),
		acp.WithStartLocations(toolLocations(p.root, tool)),
		acp.WithStartRawInput(decodeJSONValue(trace.Content)),
		acp.WithStartContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(acpTraceInput(trace)))}),
	)
	update.ToolCall.Meta = acpTraceMeta(trace)
	key := traceKey(trace)
	p.pending[key] = append(p.pending[key], call)
	return p.emit(ctx, update)
}

func (p *acpPlanTrace) finish(ctx context.Context, key acpTraceKey, output string, failed bool) error {
	queue := p.pending[key]
	call := queue[0]
	status, heading := acp.ToolCallStatusCompleted, "Result:\n"
	if failed {
		status, heading = acp.ToolCallStatusFailed, "Error:\n"
	}
	update := acp.UpdateToolCall(call.id,
		acp.WithUpdateStatus(status),
		acp.WithUpdateRawOutput(decodeJSONValue(output)),
		// ACP updates replace content. Keep the input visible alongside the
		// exact result, including Loom receipts and errors seen by the model.
		acp.WithUpdateContent([]acp.ToolCallContent{
			acp.ToolContent(acp.TextBlock(acpTraceInput(call.trace))),
			acp.ToolContent(acp.TextBlock(heading + output)),
		}),
	)
	update.ToolCallUpdate.Meta = acpTraceMeta(call.trace)
	if err := p.emit(ctx, update); err != nil {
		return err
	}
	if len(queue) == 1 {
		delete(p.pending, key)
	} else {
		p.pending[key] = queue[1:]
	}
	return nil
}

func (p *acpPlanTrace) finishPending(ctx context.Context, reason string) error {
	var calls []acpTraceCall
	for _, queue := range p.pending {
		calls = append(calls, queue...)
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].sequence < calls[j].sequence })
	for _, call := range calls {
		if err := p.finish(ctx, traceKey(call.trace), reason, true); err != nil {
			// A failed transport cannot reliably complete subsequent updates.
			return err
		}
	}
	return nil
}

func traceKey(trace agentTrace) acpTraceKey {
	return acpTraceKey{trace.Agent, trace.TaskID, trace.ParentID, trace.CallID, trace.Name}
}

func acpTraceAgentLabel(agent, taskID string) string {
	if taskID != "" {
		return agent + " [" + taskID + "]"
	}
	return agent
}

func acpTraceInput(trace agentTrace) string {
	identity := "Agent: " + trace.Agent
	if trace.TaskID != "" {
		identity += "\nTask: " + trace.TaskID
	}
	if trace.ParentID != "" {
		identity += "\nParent task: " + trace.ParentID
	}
	identity += "\nTool: " + trace.Name
	if trace.CallID != "" {
		identity += "\nModel call ID: " + trace.CallID
	}
	return identity + "\nInput:\n" + trace.Content
}

func acpTraceMeta(trace agentTrace) map[string]any {
	return map[string]any{"q": map[string]any{
		"agent": trace.Agent, "taskId": trace.TaskID, "parentTaskId": trace.ParentID,
		"callId": trace.CallID, "toolName": trace.Name,
	}}
}
