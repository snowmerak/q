package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/workitems"
)

const ticketQuestionTool = "ask_to_user"

func ticketQuestionDefinition() client.Tool {
	return client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
		Name: ticketQuestionTool, Description: "Request one material decision in the project inbox and save this worker session. Call alone, before changing any work that depends on the answer. Use late=true if the decision only became necessary after implementation or verification.",
		Parameters: map[string]any{"type": "object", "required": []string{"question"}, "additionalProperties": false,
			"properties": map[string]any{
				"question": map[string]any{"type": "string"},
				"context":  map[string]any{"type": "string", "description": "Relevant evidence, recommended choice, consequences and what is waiting"},
				"choices":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 5},
				"late":     map[string]any{"type": "boolean"},
			}},
	}}
}

func (r *GeneralRunner) storeTicketQuestion(ctx context.Context, state *GeneralRunState, history *ContextCompactor, call client.ToolCall, checkpoint func() error) (TaskResult, error) {
	var q workitems.UserQuestion
	if err := json.Unmarshal([]byte(call.Function.Arguments), &q); err != nil || q.Validate() != nil || q.ID != "" || q.Answer != "" {
		return TaskResult{}, workitems.ErrInvalidInput
	}
	if err := r.sealTicketGuidance(ctx); err != nil {
		return TaskResult{}, err
	}
	q.Late = q.Late || questionFollowsChanges(state.Transcript)
	message := client.ToolResultMessage(call, client.ToolResult{Content: `{"status":"waiting_for_user"}`})
	history.Append(message)
	if q.Late {
		if err := history.compact(ctx, &r.Spec, r.Client, true); err != nil && !errors.Is(err, memory.ErrNothingToCompact) {
			return TaskResult{}, fmt.Errorf("compact before posting the ticket question: %w", err)
		}
	}
	state.Transcript = append(state.Transcript, message)
	if err := checkpoint(); err != nil {
		return TaskResult{}, err
	}
	return TaskResult{Outcome: workitems.WaitingUser, Summary: q.Question, UserQuestion: &q}, nil
}

func questionFollowsChanges(messages []client.Message) bool {
	for _, message := range messages {
		if message.Role != client.RoleTool {
			continue
		}
		switch message.Name {
		case TaskStartToolName, ticketQuestionTool, "read_file", "list_directory", "search", "search_work", "post_get", "ticket_get", "issue_get", "ticket_attempt_get", "ticket_change_read", "ticket_worker_read", "workspace_members", "skill_get", "skill_search":
		default:
			return true
		}
	}
	return false
}

func PendingTicketQuestion(messages []client.Message) bool {
	calls, completed := pendingGeneralTools(messages)
	return len(calls) == 1 && completed == 0 && calls[0].Function.Name == ticketQuestionTool
}
