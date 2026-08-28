package subagent

import (
	"context"
	"errors"
	"strings"

	"github.com/snowmerak/q/client"
)

// FinishToolTurn acknowledges a terminal result on this execution's selected
// provider, without starting another role execution or compacting away the
// pending call/result pair.
func (s *Spec) FinishToolTurn(ctx context.Context, configured client.ModelChatClient, request client.ChatRequest, observe func(client.Message) error) (client.ToolTurnResult, error) {
	if s == nil {
		return client.ToolTurnResult{}, errors.New("subagent: model spec is nil")
	}
	s.Apply(&request)
	if request.ConversationID == "" {
		request.ConversationID = s.conversationID
	}
	return client.FinishToolTurn(ctx, request, func(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		// A different candidate cannot acknowledge this provider's pending call.
		// Preserve its timeout, but never fall back during terminal delivery.
		if s.Group != "" && s.activeCandidate >= 0 && s.activeCandidate < len(s.Candidates) && s.Candidates[s.activeCandidate].Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, s.Candidates[s.activeCandidate].Timeout)
			defer cancel()
		}
		response, err := configured.Chat(ctx, request)
		if err == nil && response != nil && response.ConversationID != "" {
			s.conversationID = response.ConversationID
		}
		return response, err
	}, observe)
}

func finishRoleTool(ctx context.Context, configured AgentClient, spec *Spec, history *ContextCompactor,
	request client.ChatRequest, call client.ToolCall, result client.ToolResult,
	trace TraceFunc, agent, taskID, parentID string, lifecycle *Lifecycle,
) (client.Usage, error) {
	record := func(message client.Message) error {
		history.Append(message)
		if message.Role == client.RoleAssistant {
			traceAssistant(trace, agent, taskID, parentID, message)
		} else {
			traceToolResult(trace, agent, taskID, parentID, client.ToolCall{
				ID: message.ToolCallID, Function: client.FunctionCall{Name: message.Name},
			}, client.ToolResult{Content: message.Content, IsError: strings.HasPrefix(message.Content, "Tool error:")})
		}
		if lifecycle != nil {
			return lifecycle.Message(message)
		}
		return nil
	}
	if err := record(client.ToolResultMessage(call, result)); err != nil {
		return client.Usage{}, err
	}
	request.Messages = history.RequestMessages()
	finished, err := spec.FinishToolTurn(ctx, configured, request, record)
	return finished.Usage, err
}
