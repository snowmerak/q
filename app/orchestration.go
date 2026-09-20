package app

import (
	"strings"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
)

const (
	askToUserToolName       = agentloop.AskToUserToolName
	taskStartToolName       = agentloop.TaskStartToolName
	taskCompleteToolName    = agentloop.TaskCompleteToolName
	customAnswerLabel       = "Write a custom answer"
	maximumAskToUserChoices = agentloop.MaximumAskToUserChoices
)

type taskStartInput = agentloop.TaskStart
type askToUserChoice = agentloop.Choice
type askToUserInput = agentloop.Question
type taskCompleteInput = agentloop.Completion

type askToUserOutput struct {
	SelectedChoiceID string        `json:"selected_choice_id,omitempty"`
	Freeform         string        `json:"freeform,omitempty"`
	SkillHints       *skillHintSet `json:"skill_hints,omitempty"`
	Err              error         `json:"-"`
}

func orchestrationTools() []client.Tool {
	return agentloop.OrchestrationTools()
}

func parseTaskStart(arguments string) (taskStartInput, error) {
	return agentloop.ParseTaskStart(arguments)
}

func parseAskToUser(arguments string) (askToUserInput, error) {
	return agentloop.ParseQuestion(arguments)
}

func parseTaskComplete(arguments string) (taskCompleteInput, error) {
	return agentloop.ParseCompletion(arguments)
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
	return agentloop.RenderCompletion(input)
}

func renderPendingQuestion(input askToUserInput, selected int) string {
	var body strings.Builder
	body.WriteString(input.Question)
	if input.Context != "" {
		body.WriteString("\n")
		body.WriteString(input.Context)
	}
	for index, choice := range input.Choices {
		body.WriteString("\n")
		label := choice.ID + " · " + choice.Label
		if index == selected {
			body.WriteString(activeLabelStyle.Render("› " + label))
		} else {
			body.WriteString("  " + label)
		}
		if choice.Description != "" {
			body.WriteString(" — ")
			body.WriteString(choice.Description)
		}
	}
	if len(input.Choices) > 0 && !input.ChoiceOnly {
		body.WriteString("\n")
		label := customAnswerLabel
		if selected == len(input.Choices) {
			body.WriteString(activeLabelStyle.Render("› " + label))
		} else {
			body.WriteString("  " + label)
		}
		body.WriteString(" — type below and press enter")
	}
	return body.String()
}

func questionChoiceCount(input askToUserInput) int {
	if len(input.Choices) == 0 {
		return 0
	}
	if input.ChoiceOnly {
		return len(input.Choices)
	}
	return len(input.Choices) + 1
}

func customAnswerSelected(input askToUserInput, selected int) bool {
	return len(input.Choices) > 0 && !input.ChoiceOnly && selected == len(input.Choices)
}
