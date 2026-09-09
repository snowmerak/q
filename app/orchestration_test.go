package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOrchestrationToolsExposeConditionalTaskLifecycle(t *testing.T) {
	available := orchestrationTools()
	if len(available) != 3 || available[0].Function.Name != taskStartToolName ||
		available[1].Function.Name != askToUserToolName || available[2].Function.Name != taskCompleteToolName {
		t.Fatalf("orchestration tools = %#v", available)
	}
	if !strings.Contains(available[0].Function.Description, "Direct, short answers do not need") ||
		!strings.Contains(available[2].Function.Description, "previously started") {
		t.Fatalf("task lifecycle descriptions = start %q, complete %q", available[0].Function.Description, available[2].Function.Description)
	}
}

func TestTaskStartRequiresObjective(t *testing.T) {
	started, err := parseTaskStart(`{"objective":"Implement the feature","completion_criteria":["Tests pass"]}`)
	if err != nil || started.Objective != "Implement the feature" {
		t.Fatalf("task start = %#v, err = %v", started, err)
	}
	if _, err := parseTaskStart(`{"objective":" "}`); err == nil {
		t.Fatal("task_start without objective was accepted")
	}
}

func TestAskToUserArgumentsAndChoiceAnswer(t *testing.T) {
	input, err := parseAskToUser(`{
		"question":"Choose a mode",
		"choices":[{"id":"safe","label":"Safe mode"}]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	answer := answerForQuestion(input, "Safe mode")
	if answer.SelectedChoiceID != "safe" || answer.Freeform != "" {
		t.Fatalf("answer = %#v", answer)
	}
	custom := answerForQuestion(input, "Use the current repository convention")
	if custom.SelectedChoiceID != "" || custom.Freeform != "Use the current repository convention" {
		t.Fatalf("custom answer = %#v", custom)
	}
	rendered := renderPendingQuestion(input, len(input.Choices))
	if !strings.Contains(rendered, customAnswerLabel) || !strings.Contains(rendered, "type below") {
		t.Fatalf("custom answer choice was not rendered last: %q", rendered)
	}
	if _, err := parseAskToUser(`{"question":"","unexpected":true}`); err == nil {
		t.Fatal("invalid ask_to_user arguments were accepted")
	}
}

func TestAskToUserAcceptsAtMostNineChoices(t *testing.T) {
	properties := orchestrationTools()[1].Function.Parameters["properties"].(map[string]any)
	choiceSchema := properties["choices"].(map[string]any)
	if got := choiceSchema["maxItems"]; got != maximumAskToUserChoices {
		t.Fatalf("ask_to_user maxItems = %v; want %d", got, maximumAskToUserChoices)
	}

	choices := make([]askToUserChoice, maximumAskToUserChoices)
	for index := range choices {
		id := string(rune('a' + index))
		choices[index] = askToUserChoice{ID: id, Label: "Choice " + id}
	}
	arguments, err := json.Marshal(askToUserInput{Question: "Choose", Choices: choices})
	if err != nil {
		t.Fatal(err)
	}
	input, err := parseAskToUser(string(arguments))
	if err != nil || len(input.Choices) != maximumAskToUserChoices {
		t.Fatalf("nine choices = %#v, err = %v", input.Choices, err)
	}

	choices = append(choices, askToUserChoice{ID: "j", Label: "Choice j"})
	arguments, err = json.Marshal(askToUserInput{Question: "Choose", Choices: choices})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseAskToUser(string(arguments)); err == nil || !strings.Contains(err.Error(), "at most 9") {
		t.Fatalf("ten choices error = %v", err)
	}
}

func TestTaskCompleteRequiresTerminalOutcome(t *testing.T) {
	completion, err := parseTaskComplete(`{
		"outcome":"succeeded",
		"summary":"Implemented the requested change",
		"verification":["go test ./..."]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if rendered := renderTaskCompletion(completion); !strings.Contains(rendered, "Implemented") ||
		!strings.Contains(rendered, "go test ./...") {
		t.Fatalf("rendered completion = %q", rendered)
	}
	if _, err := parseTaskComplete(`{"outcome":"blocked","summary":"Cannot continue"}`); err == nil {
		t.Fatal("blocked completion without blocker was accepted")
	}
}
