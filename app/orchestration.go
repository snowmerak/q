package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/snowmerak/q/client"
)

const (
	askToUserToolName    = "ask_to_user"
	taskCompleteToolName = "task_complete"
)

type askToUserChoice struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type askToUserInput struct {
	Question string            `json:"question"`
	Context  string            `json:"context,omitempty"`
	Choices  []askToUserChoice `json:"choices,omitempty"`
}

type askToUserOutput struct {
	SelectedChoiceID string `json:"selected_choice_id,omitempty"`
	Freeform         string `json:"freeform,omitempty"`
}

type taskCompleteInput struct {
	Outcome      string   `json:"outcome"`
	Summary      string   `json:"summary"`
	Findings     []string `json:"findings,omitempty"`
	Artifacts    []string `json:"artifacts,omitempty"`
	Verification []string `json:"verification,omitempty"`
	Blocker      string   `json:"blocker,omitempty"`
}

func orchestrationTools() []client.Tool {
	return []client.Tool{
		{
			Type: client.ToolTypeFunction,
			Function: client.FunctionDefinition{
				Name:        askToUserToolName,
				Description: "Pause the current task and ask the user one necessary question. The task resumes after the user answers.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": map[string]any{"type": "string"},
						"context":  map[string]any{"type": "string"},
						"choices": map[string]any{
							"type": "array", "maxItems": 3,
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
				Description: "Finish the current task and return its structured result. Every task must end with exactly one successful task_complete call.",
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
	if len(input.Choices) > 3 {
		return askToUserInput{}, errors.New("choices must contain at most 3 items")
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

func renderPendingQuestion(input askToUserInput) string {
	var body strings.Builder
	body.WriteString(input.Question)
	if input.Context != "" {
		body.WriteString("\n")
		body.WriteString(input.Context)
	}
	for _, choice := range input.Choices {
		body.WriteString("\n")
		body.WriteString(choice.ID)
		body.WriteString(" · ")
		body.WriteString(choice.Label)
		if choice.Description != "" {
			body.WriteString(" — ")
			body.WriteString(choice.Description)
		}
	}
	return body.String()
}
