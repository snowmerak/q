package agentloop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/snowmerak/q/client"
)

const (
	TaskStartToolName    = "task_start"
	AskToUserToolName    = "ask_to_user"
	TaskCompleteToolName = "task_complete"

	MaximumAskToUserChoices = 9
)

// TaskStart is the validated task_start input.
type TaskStart struct {
	Objective          string   `json:"objective"`
	CompletionCriteria []string `json:"completion_criteria,omitempty"`
}

type taskStartOutput struct {
	Started            bool          `json:"started"`
	Objective          string        `json:"objective"`
	CompletionCriteria []string      `json:"completion_criteria,omitempty"`
	SkillHints         *SkillHintSet `json:"skill_hints,omitempty"`
}

// Choice is one suggested answer to a Question.
type Choice struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Question is the validated ask_to_user input. ChoiceOnly is host metadata and
// is not sent by the model.
type Question struct {
	Question   string   `json:"question"`
	Context    string   `json:"context,omitempty"`
	Choices    []Choice `json:"choices,omitempty"`
	ChoiceOnly bool     `json:"-"`
}

// Answer is supplied by Hooks.Ask and encoded as the ask_to_user tool result.
type Answer struct {
	SelectedChoiceID string `json:"selected_choice_id,omitempty"`
	Freeform         string `json:"freeform,omitempty"`
}

type answerOutput struct {
	Answer
	SkillHints *SkillHintSet `json:"skill_hints,omitempty"`
}

// Completion is the validated task_complete input and structured result.
type Completion struct {
	Outcome      string   `json:"outcome"`
	Summary      string   `json:"summary"`
	Findings     []string `json:"findings,omitempty"`
	Artifacts    []string `json:"artifacts,omitempty"`
	Verification []string `json:"verification,omitempty"`
	Blocker      string   `json:"blocker,omitempty"`
}

// OrchestrationTools returns fresh task lifecycle tool definitions. These
// names are reserved by Run and must not be supplied by ToolRuntime.Tools.
func OrchestrationTools() []client.Tool {
	return []client.Tool{
		{
			Type: client.ToolTypeFunction,
			Function: client.FunctionDefinition{
				Name:        TaskStartToolName,
				Description: "Start an explicit task lifecycle for work that requires tools or multiple steps. Direct, short answers do not need task_start. Once started, the task must end with task_complete.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"objective":           map[string]any{"type": "string"},
						"completion_criteria": stringArraySchema(),
					},
					"required":             []string{"objective"},
					"additionalProperties": false,
				},
			},
		},
		{
			Type: client.ToolTypeFunction,
			Function: client.FunctionDefinition{
				Name:        AskToUserToolName,
				Description: "Pause the current task and ask the user one necessary question. Choices are optional, non-exhaustive answer suggestions; the user may always answer in free text. The task resumes after the user answers.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": map[string]any{"type": "string"},
						"context":  map[string]any{"type": "string"},
						"choices": map[string]any{
							"type": "array", "maxItems": MaximumAskToUserChoices,
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"id":          map[string]any{"type": "string"},
									"label":       map[string]any{"type": "string"},
									"description": map[string]any{"type": "string"},
								},
								"required":             []string{"id", "label"},
								"additionalProperties": false,
							},
						},
					},
					"required":             []string{"question"},
					"additionalProperties": false,
				},
			},
		},
		{
			Type: client.ToolTypeFunction,
			Function: client.FunctionDefinition{
				Name:        TaskCompleteToolName,
				Description: "Finish the active task previously started with task_start and return its structured result. A restored task may have started in an earlier turn.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"outcome":      map[string]any{"type": "string", "enum": []string{"succeeded", "blocked"}},
						"summary":      map[string]any{"type": "string"},
						"findings":     stringArraySchema(),
						"artifacts":    stringArraySchema(),
						"verification": stringArraySchema(),
						"blocker":      map[string]any{"type": "string"},
					},
					"required":             []string{"outcome", "summary"},
					"additionalProperties": false,
				},
			},
		},
	}
}

// ParseTaskStart validates strict task_start JSON arguments.
func ParseTaskStart(arguments string) (TaskStart, error) {
	var input TaskStart
	if err := decodeToolArguments(arguments, &input); err != nil {
		return TaskStart{}, err
	}
	input.Objective = strings.TrimSpace(input.Objective)
	if input.Objective == "" {
		return TaskStart{}, errors.New("objective is required")
	}
	for index := range input.CompletionCriteria {
		input.CompletionCriteria[index] = strings.TrimSpace(input.CompletionCriteria[index])
	}
	return input, nil
}

// ParseQuestion validates strict ask_to_user JSON arguments.
func ParseQuestion(arguments string) (Question, error) {
	var input Question
	if err := decodeToolArguments(arguments, &input); err != nil {
		return Question{}, err
	}
	input.Question = strings.TrimSpace(input.Question)
	input.Context = strings.TrimSpace(input.Context)
	if input.Question == "" {
		return Question{}, errors.New("question is required")
	}
	if len(input.Choices) > MaximumAskToUserChoices {
		return Question{}, fmt.Errorf("choices must contain at most %d items", MaximumAskToUserChoices)
	}
	seen := make(map[string]struct{}, len(input.Choices))
	for index := range input.Choices {
		choice := &input.Choices[index]
		choice.ID = strings.TrimSpace(choice.ID)
		choice.Label = strings.TrimSpace(choice.Label)
		choice.Description = strings.TrimSpace(choice.Description)
		if choice.ID == "" || choice.Label == "" {
			return Question{}, fmt.Errorf("choice %d requires id and label", index+1)
		}
		if _, exists := seen[choice.ID]; exists {
			return Question{}, fmt.Errorf("duplicate choice id %q", choice.ID)
		}
		seen[choice.ID] = struct{}{}
	}
	return input, nil
}

// ParseCompletion validates strict task_complete JSON arguments.
func ParseCompletion(arguments string) (Completion, error) {
	var input Completion
	if err := decodeToolArguments(arguments, &input); err != nil {
		return Completion{}, err
	}
	input.Outcome = strings.TrimSpace(input.Outcome)
	input.Summary = strings.TrimSpace(input.Summary)
	input.Blocker = strings.TrimSpace(input.Blocker)
	if input.Outcome != "succeeded" && input.Outcome != "blocked" {
		return Completion{}, errors.New("outcome must be succeeded or blocked")
	}
	if input.Summary == "" {
		return Completion{}, errors.New("summary is required")
	}
	if input.Outcome == "blocked" && input.Blocker == "" {
		return Completion{}, errors.New("blocker is required when outcome is blocked")
	}
	return input, nil
}

func stringArraySchema() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
}

func decodeToolArguments(arguments string, output any) error {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode arguments: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("decode arguments: multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode arguments: %w", err)
	}
	return nil
}

// RenderCompletion formats the structured terminal result as q's final
// assistant response.
func RenderCompletion(input Completion) string {
	var body strings.Builder
	body.WriteString(input.Summary)
	writeCompletionList(&body, "Findings", input.Findings)
	writeCompletionList(&body, "Artifacts", input.Artifacts)
	writeCompletionList(&body, "Verification", input.Verification)
	if input.Blocker != "" {
		body.WriteString("\n\nBlocker: ")
		body.WriteString(input.Blocker)
	}
	return body.String()
}

func writeCompletionList(body *strings.Builder, label string, values []string) {
	if len(values) == 0 {
		return
	}
	body.WriteString("\n\n")
	body.WriteString(label)
	body.WriteString(":")
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			body.WriteString("\n- ")
			body.WriteString(value)
		}
	}
}

func orchestrationToolResult(call client.ToolCall, content string, isError bool) client.Message {
	if isError {
		content = "Tool error: " + content
	}
	return client.Message{
		Role: client.RoleTool, Name: call.Function.Name,
		ToolCallID: call.ID, Content: content,
	}
}
