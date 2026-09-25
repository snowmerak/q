package app

import (
	"strings"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
)

const (
	askToUserToolName       = "ask_to_user"
	taskStartToolName       = "task_start"
	taskCompleteToolName    = "task_complete"
	customAnswerLabel       = "Write a custom answer"
	maximumAskToUserChoices = 9
)

type taskStartInput = agentloop.TaskStartInput
type taskCompleteInput = agentloop.TaskCompleteInput
type AgentQuestionChoice = agentloop.AgentQuestionChoice
type askToUserChoice = AgentQuestionChoice
type AgentQuestion = agentloop.AgentQuestion
type askToUserInput = AgentQuestion
type AgentAnswer = agentloop.AgentAnswer
type askToUserOutput = AgentAnswer

func orchestrationTools() []client.Tool { return agentloop.OrchestrationTools() }
func parseTaskStart(arguments string) (taskStartInput, error) {
	return agentloop.ParseTaskStart(arguments)
}
func parseAskToUser(arguments string) (askToUserInput, error) {
	return agentloop.ParseAskToUser(arguments)
}
func parseTaskComplete(arguments string) (taskCompleteInput, error) {
	return agentloop.ParseTaskComplete(arguments)
}
func answerForQuestion(input askToUserInput, answer string) askToUserOutput {
	return agentloop.AnswerForQuestion(input, answer)
}
func renderTaskCompletion(input taskCompleteInput) string {
	return agentloop.RenderTaskCompletion(input)
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
