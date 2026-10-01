package sessionstore

import (
	"context"
	"encoding/json"
	"slices"
)

// MessageToolCall identifies an assistant response using its existing tool call
// ID, scoped to a session and agent. An empty TaskID denotes the root agent.
type MessageToolCall struct {
	RunID      string `json:"run_id"`
	TaskID     string `json:"task_id,omitempty"`
	ToolCallID string `json:"tool_call_id"`
}

type searchScopeKey struct{}

type searchScope struct {
	runID  string
	taskID string
}

// WithSearchScope identifies the session and agent issuing archive searches.
// An empty taskID denotes root chat. Each child must bind its own scope even
// when it inherits its parent's context and shares the same tool runtime.
func WithSearchScope(ctx context.Context, runID, taskID string) context.Context {
	return context.WithValue(ctx, searchScopeKey{}, searchScope{runID: runID, taskID: taskID})
}

// SearchMessageExclusion identifies the response that issued a tool call.
// Unscoped calls return nil and retain the full archive search behavior.
func SearchMessageExclusion(ctx context.Context, toolCallID string) *MessageToolCall {
	scope, ok := ctx.Value(searchScopeKey{}).(searchScope)
	if !ok || scope.runID == "" || toolCallID == "" {
		return nil
	}
	return &MessageToolCall{RunID: scope.runID, TaskID: scope.taskID, ToolCallID: toolCallID}
}

func (message *MessageToolCall) matches(record Record) bool {
	if message == nil || message.RunID == "" || message.ToolCallID == "" ||
		record.Kind != KindMessage || record.RunID != message.RunID || record.TaskID != message.TaskID {
		return false
	}
	// Root chat wraps the provider message in an envelope; subagents store it
	// directly. Both already persist the tool call IDs, including older archives.
	type payloadMessage struct {
		Role      string `json:"role"`
		ToolCalls []struct {
			ID string `json:"id"`
		} `json:"tool_calls"`
	}
	var payload struct {
		payloadMessage
		Message *payloadMessage `json:"message"`
	}
	if json.Unmarshal(record.Payload, &payload) != nil {
		return false
	}
	assistant := payload.payloadMessage
	if payload.Message != nil {
		assistant = *payload.Message
	}
	if assistant.Role != "assistant" {
		return false
	}
	for _, call := range assistant.ToolCalls {
		if call.ID == message.ToolCallID {
			return true
		}
	}
	return false
}

func omitToolCallMessage(hits []Hit, message *MessageToolCall) []Hit {
	if message == nil {
		return hits
	}
	latest := -1
	for index, hit := range hits {
		if message.matches(hit.Record) && (latest < 0 || hit.Record.CreatedAt.After(hits[latest].Record.CreatedAt)) {
			latest = index
		}
	}
	if latest >= 0 {
		return slices.Delete(hits, latest, latest+1)
	}
	return hits
}
