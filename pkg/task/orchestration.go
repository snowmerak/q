package task

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/snowmerak/q/v2/pkg/domain"
)

func registerOrchestrationTools(r *Registry) {
	r.register(ToolSpec{
		Name:             "ask_to_user",
		Description:      "Pause execution cleanly to ask the user a clarifying question with optional preset choices.",
		Source:           domain.ToolSourceBuiltin,
		Risk:             RiskRead,
		SkipApprovalGate: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt":         map[string]any{"type": "string"},
				"choices":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"allow_freeform": map[string]any{"type": "boolean"},
			},
			"required": []string{"prompt"},
		},
		Handler: func(_ context.Context, call CallContext, argsJSON string) (ToolOutput, error) {
			var args struct {
				Prompt        string   `json:"prompt"`
				Choices       []string `json:"choices"`
				AllowFreeform *bool    `json:"allow_freeform"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			prompt := strings.TrimSpace(args.Prompt)
			if prompt == "" {
				return ToolOutput{Content: "prompt is required", IsError: true}, nil
			}
			allowFree := true
			if args.AllowFreeform != nil {
				allowFree = *args.AllowFreeform
			}
			return ToolOutput{
				Content:   "Waiting for user response to: " + prompt,
				PauseKind: "awaiting_user",
				UserQuestion: &domain.UserQuestion{
					ID:            domain.NewID(),
					TurnID:        call.TurnID,
					RoomID:        call.RoomID,
					ActorID:       call.ActorID,
					Prompt:        prompt,
					Choices:       args.Choices,
					AllowFreeform: allowFree,
					Status:        domain.QuestionStatusPending,
					CreatedAt:     domain.NowMS(),
				},
			}, nil
		},
	})

	r.register(ToolSpec{
		Name:        "task_start",
		Description: "Announce progress on a structured subtask or checklist step to the live room stream.",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskRead,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{"type": "string"},
			},
			"required": []string{"title"},
		},
		Handler: func(_ context.Context, call CallContext, argsJSON string) (ToolOutput, error) {
			var args struct {
				Title string `json:"title"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			if call.ProgressFn != nil && strings.TrimSpace(args.Title) != "" {
				call.ProgressFn("Started task: " + strings.TrimSpace(args.Title))
			}
			return ToolOutput{Content: "Task started: " + strings.TrimSpace(args.Title)}, nil
		},
	})

	r.register(ToolSpec{
		Name:        "task_complete",
		Description: "Mark a structured subtask complete with a brief outcome summary.",
		Source:      domain.ToolSourceBuiltin,
		Risk:        RiskRead,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":   map[string]any{"type": "string"},
				"summary": map[string]any{"type": "string"},
			},
			"required": []string{"title"},
		},
		Handler: func(_ context.Context, call CallContext, argsJSON string) (ToolOutput, error) {
			var args struct {
				Title   string `json:"title"`
				Summary string `json:"summary"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return ToolOutput{Content: "invalid args: " + err.Error(), IsError: true}, nil
			}
			if call.ProgressFn != nil {
				call.ProgressFn("Completed task: " + strings.TrimSpace(args.Title))
			}
			return ToolOutput{Content: fmt.Sprintf("Task completed: %s (%s)", strings.TrimSpace(args.Title), strings.TrimSpace(args.Summary))}, nil
		},
	})
}
