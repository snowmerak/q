package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

func TestSprintConfigForcesAutomationWithoutMutatingInput(t *testing.T) {
	configured := config.Default()
	configured.Plan = config.PlanConfig{AutoApprove: false, AutoResolve: false}
	effective := sprintConfig(configured)
	if !effective.Plan.AutoApprove || !effective.Plan.AutoResolve {
		t.Fatalf("sprint config = %#v", effective.Plan)
	}
	if configured.Plan.AutoApprove || configured.Plan.AutoResolve {
		t.Fatalf("input config was mutated = %#v", configured.Plan)
	}
}

func TestSprintModelPrintsFinalPlanResultAndQuits(t *testing.T) {
	state := newModel(context.Background(), config.Store{}, nil)
	initial := func() tea.Msg {
		return agentEventMsg{event: agentEvent{response: &client.ChatResponse{Choices: []client.Choice{{
			Message: client.Message{Role: client.RoleAssistant, Content: "Sprint finished successfully."},
		}}}}}
	}
	var output bytes.Buffer
	final, err := tea.NewProgram(
		sprintModel{state: state, initial: initial, output: &output},
		tea.WithInput(nil), tea.WithOutput(&output), tea.WithoutRenderer(), tea.WithoutSignalHandler(),
	).Run()
	if err != nil {
		t.Fatal(err)
	}
	result, ok := final.(sprintModel)
	if !ok || result.err != nil {
		t.Fatalf("final sprint model = %#v", final)
	}
	if !strings.Contains(output.String(), "Sprint finished successfully.") {
		t.Fatalf("sprint output = %q", output.String())
	}
}

func TestSprintModelFailsInsteadOfWaitingForUserInput(t *testing.T) {
	state := newModel(context.Background(), config.Store{}, nil)
	answers := make(chan askToUserOutput, 1)
	initial := func() tea.Msg {
		return agentEventMsg{event: agentEvent{
			question: &askToUserInput{Question: "Choose a product decision"},
			answer:   answers,
		}}
	}
	final, err := tea.NewProgram(
		sprintModel{state: state, initial: initial},
		tea.WithInput(nil), tea.WithOutput(&bytes.Buffer{}), tea.WithoutRenderer(), tea.WithoutSignalHandler(),
	).Run()
	if err != nil {
		t.Fatal(err)
	}
	result := final.(sprintModel)
	if result.err == nil || !strings.Contains(result.err.Error(), "unexpectedly requested user input") {
		t.Fatalf("sprint error = %v", result.err)
	}
	answer := <-answers
	if !errors.Is(answer.Err, result.err) {
		t.Fatalf("question answer error = %v, want %v", answer.Err, result.err)
	}
}
