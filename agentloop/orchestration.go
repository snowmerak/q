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
	askToUserToolName       = "ask_to_user"
	taskStartToolName       = "task_start"
	taskCompleteToolName    = "task_complete"
	customAnswerLabel       = "Write a custom answer"
	maximumAskToUserChoices = 9
)

type taskStartInput struct {
	Objective          string   `json:"objective"`
	CompletionCriteria []string `json:"completion_criteria,omitempty"`
}

// TaskStartInput is the parsed task_start payload.
type TaskStartInput = taskStartInput

type taskStartOutput struct {
	Started            bool          `json:"started"`
	Objective          string        `json:"objective"`
	CompletionCriteria []string      `json:"completion_criteria,omitempty"`
	SkillHints         *skillHintSet `json:"skill_hints,omitempty"`
}

// AgentQuestionChoice is one suggested answer to an AgentQuestion.
type AgentQuestionChoice struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type askToUserChoice = AgentQuestionChoice

// AgentQuestion describes a model-requested interaction. ChoiceOnly is used
// by permission-style hosts that cannot accept a free-form answer.
type AgentQuestion struct {
	Question   string                `json:"question"`
	Context    string                `json:"context,omitempty"`
	Choices    []AgentQuestionChoice `json:"choices,omitempty"`
	ChoiceOnly bool                  `json:"-"`
}

type askToUserInput = AgentQuestion

// AgentAnswer resumes an AgentQuestion. ErrInteractionUnavailable asks the
// loop to return a recoverable tool error to the model; other errors abort it.
type AgentAnswer struct {
	SelectedChoiceID string        `json:"selected_choice_id,omitempty"`
	Freeform         string        `json:"freeform,omitempty"`
	SkillHints       *SkillHintSet `json:"skill_hints,omitempty"`
	Err              error         `json:"-"`
}

type askToUserOutput = AgentAnswer

type taskCompleteInput struct {
	Outcome      string   `json:"outcome"`
	Summary      string   `json:"summary"`
	Findings     []string `json:"findings,omitempty"`
	Artifacts    []string `json:"artifacts,omitempty"`
	Verification []string `json:"verification,omitempty"`
	Blocker      string   `json:"blocker,omitempty"`
}

// TaskCompleteInput is the parsed task_complete payload.
type TaskCompleteInput = taskCompleteInput

// OrchestrationTools returns Q's task and question tools.
func OrchestrationTools() []client.Tool { return orchestrationTools() }

// ParseTaskStart validates a task_start payload.
func ParseTaskStart(arguments string) (TaskStartInput, error) { return parseTaskStart(arguments) }

// ParseAskToUser validates an ask_to_user payload.
func ParseAskToUser(arguments string) (AgentQuestion, error) { return parseAskToUser(arguments) }

// ParseTaskComplete validates a task_complete payload.
func ParseTaskComplete(arguments string) (TaskCompleteInput, error) {
	return parseTaskComplete(arguments)
}

// AnswerForQuestion maps a host answer to the selected choice or free text.
func AnswerForQuestion(input AgentQuestion, answer string) AgentAnswer {
	return answerForQuestion(input, answer)
}

// RenderTaskCompletion formats the task_complete result for a user.
func RenderTaskCompletion(input TaskCompleteInput) string { return renderTaskCompletion(input) }

func orchestrationTools() []client.Tool {
	return []client.Tool{
		{
			Type: client.ToolTypeFunction,
			Function: client.FunctionDefinition{
				Name:        taskStartToolName,
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
				Name:        askToUserToolName,
				Description: "Pause the current task and ask the user one necessary question. Choices are optional, non-exhaustive answer suggestions; the user may always answer in free text. The task resumes after the user answers.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": map[string]any{"type": "string"},
						"context":  map[string]any{"type": "string"},
						"choices": map[string]any{
							"type": "array", "maxItems": maximumAskToUserChoices,
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
				Name:        taskCompleteToolName,
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

func parseTaskStart(arguments string) (taskStartInput, error) {
	var input taskStartInput
	if err := decodeToolArguments(arguments, &input); err != nil {
		return taskStartInput{}, err
	}
	input.Objective = strings.TrimSpace(input.Objective)
	if input.Objective == "" {
		return taskStartInput{}, errors.New("objective is required")
	}
	for index := range input.CompletionCriteria {
		input.CompletionCriteria[index] = strings.TrimSpace(input.CompletionCriteria[index])
	}
	return input, nil
}

func stringArraySchema() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
}

func parseAskToUser(arguments string) (askToUserInput, error) {
	var input askToUserInput
	if err := decodeToolArguments(arguments, &input); err != nil {
		return askToUserInput{}, err
	}
	input.Question = strings.TrimSpace(input.Question)
	input.Context = strings.TrimSpace(input.Context)
	if input.Question == "" {
		return askToUserInput{}, errors.New("question is required")
	}
	if len(input.Choices) > maximumAskToUserChoices {
		return askToUserInput{}, fmt.Errorf("choices must contain at most %d items", maximumAskToUserChoices)
	}
	seen := make(map[string]struct{}, len(input.Choices))
	for index := range input.Choices {
		choice := &input.Choices[index]
		choice.ID = strings.TrimSpace(choice.ID)
		choice.Label = strings.TrimSpace(choice.Label)
		choice.Description = strings.TrimSpace(choice.Description)
		if choice.ID == "" || choice.Label == "" {
			return askToUserInput{}, fmt.Errorf("choice %d requires id and label", index+1)
		}
		if _, exists := seen[choice.ID]; exists {
			return askToUserInput{}, fmt.Errorf("duplicate choice id %q", choice.ID)
		}
		seen[choice.ID] = struct{}{}
	}
	return input, nil
}

func parseTaskComplete(arguments string) (taskCompleteInput, error) {
	var input taskCompleteInput
	if err := decodeToolArguments(arguments, &input); err != nil {
		return taskCompleteInput{}, err
	}
	input.Outcome = strings.TrimSpace(input.Outcome)
	input.Summary = strings.TrimSpace(input.Summary)
	input.Blocker = strings.TrimSpace(input.Blocker)
	if input.Outcome != "succeeded" && input.Outcome != "blocked" {
		return taskCompleteInput{}, errors.New("outcome must be succeeded or blocked")
	}
	if input.Summary == "" {
		return taskCompleteInput{}, errors.New("summary is required")
	}
	if input.Outcome == "blocked" && input.Blocker == "" {
		return taskCompleteInput{}, errors.New("blocker is required when outcome is blocked")
	}
	return input, nil
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

func answerForQuestion(input askToUserInput, answer string) askToUserOutput {
	answer = strings.TrimSpace(answer)
	for _, choice := range input.Choices {
		if answer == choice.ID || strings.EqualFold(answer, choice.Label) {
			return askToUserOutput{SelectedChoiceID: choice.ID}
		}
	}
	return askToUserOutput{Freeform: answer}
}

func renderTaskCompletion(input taskCompleteInput) string {
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
