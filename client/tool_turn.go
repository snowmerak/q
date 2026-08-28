package client

import (
	"context"
	"errors"
	"fmt"
)

const maximumTerminalRounds = 4

const terminalResultInstruction = "The host has accepted this terminal tool result. The task is finished. End this turn with a short acknowledgment and no further tool calls."

// ToolTurnResult separates the final response's usage from the total cost of
// draining a terminal tool exchange (including any rejected extra tool calls).
type ToolTurnResult struct {
	Response *ChatResponse
	Usage    Usage
}

// ToolResultMessage retains the call/result pairing used by every provider.
func ToolResultMessage(call ToolCall, result ToolResult) Message {
	content := result.Content
	if result.IsError {
		content = "Tool error: " + content
	}
	return Message{Role: RoleTool, Name: call.Function.Name, ToolCallID: call.ID, Content: content}
}

// FinishToolTurn sends the already-recorded terminal tool results upstream and
// consumes the provider's final response. It never executes more tools. Some
// stateful providers resume a parked callback before applying tool_choice, so
// unexpected calls must receive explicit rejection results rather than being
// dropped. observe records those exchanges and the final acknowledgment.
func FinishToolTurn(
	ctx context.Context,
	request ChatRequest,
	chat func(context.Context, ChatRequest) (*ChatResponse, error),
	observe func(Message) error,
) (ToolTurnResult, error) {
	var result ToolTurnResult
	if len(request.Messages) == 0 || request.Messages[len(request.Messages)-1].Role != RoleTool {
		return result, errors.New("finish tool turn: trailing tool result is required")
	}
	request.Messages = append([]Message(nil), request.Messages...)
	// This is transport-only guidance, not another user request. In particular,
	// a parked Codex callback only sees its tool output when it resumes.
	last := len(request.Messages) - 1
	request.Messages[last].Content += "\n\n" + terminalResultInstruction
	request.ToolChoice = ToolChoiceNone
	parallel := false
	request.ParallelToolCalls = &parallel
	for round := 0; round < maximumTerminalRounds; round++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		response, err := chat(ctx, request)
		if err != nil {
			return result, fmt.Errorf("finish tool turn: %w", err)
		}
		if response == nil || len(response.Choices) == 0 {
			return result, errors.New("finish tool turn: provider returned no choices")
		}
		result.Response = response
		result.Usage.PromptTokens += response.Usage.PromptTokens
		result.Usage.CompletionTokens += response.Usage.CompletionTokens
		result.Usage.TotalTokens += response.Usage.TotalTokens
		if response.ConversationID != "" {
			request.ConversationID = response.ConversationID
		}
		assistant := response.Choices[0].Message
		assistant.ToolCalls = append([]ToolCall(nil), assistant.ToolCalls...)
		if assistant.Role == "" {
			assistant.Role = RoleAssistant
		}
		for index := range assistant.ToolCalls {
			if assistant.ToolCalls[index].ID == "" {
				assistant.ToolCalls[index].ID = fmt.Sprintf("q-terminal-%d-%d", round+1, index+1)
			}
		}
		if observe != nil {
			if err := observe(assistant); err != nil {
				return result, err
			}
		}
		if len(assistant.ToolCalls) == 0 {
			reason := response.Choices[0].FinishReason
			if reason != "" && reason != "stop" {
				return result, fmt.Errorf("finish tool turn: provider stopped with %q", reason)
			}
			return result, nil
		}
		request.Messages = append(request.Messages, assistant)
		for _, call := range assistant.ToolCalls {
			message := ToolResultMessage(call, ToolResult{
				IsError: true, Content: "No tools may run after the terminal result was accepted. " + terminalResultInstruction,
			})
			request.Messages = append(request.Messages, message)
			if observe != nil {
				if err := observe(message); err != nil {
					return result, err
				}
			}
		}
	}
	return result, fmt.Errorf("finish tool turn: provider kept requesting tools after %d rounds", maximumTerminalRounds)
}
