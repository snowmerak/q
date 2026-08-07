package app

import (
	"strings"
	"testing"
)

func TestOrchestrationToolsExposeQuestionAndRequiredCompletion(t *testing.T) {
	available := orchestrationTools()
	if len(available) != 2 || available[0].Function.Name != askToUserToolName ||
		available[1].Function.Name != taskCompleteToolName {
		t.Fatalf("orchestration tools = %#v", available)
	}
	if !strings.Contains(available[1].Function.Description, "Every task must end") {
		t.Fatalf("task_complete description = %q", available[1].Function.Description)
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
	if _, err := parseAskToUser(`{"question":"","unexpected":true}`); err == nil {
		t.Fatal("invalid ask_to_user arguments were accepted")
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
