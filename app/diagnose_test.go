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

func TestDiagnoseConfigForcesOnlyAutoResolveWithoutMutatingInput(t *testing.T) {
	configured := config.Default()
	configured.Plan = config.PlanConfig{AutoApprove: false, AutoResolve: false}
	effective := diagnoseConfig(configured)
	if !effective.Plan.AutoResolve || effective.Plan.AutoApprove {
		t.Fatalf("diagnose config = %#v", effective.Plan)
	}
	if configured.Plan.AutoResolve || configured.Plan.AutoApprove {
		t.Fatalf("input config was mutated = %#v", configured.Plan)
	}
}

func TestDiagnoseModelPrintsFinalDebugReportAndQuits(t *testing.T) {
	state := newModel(context.Background(), config.Store{}, nil)
	initial := func() tea.Msg {
		return agentEventMsg{event: agentEvent{response: &client.ChatResponse{Choices: []client.Choice{{
			Message: client.Message{Role: client.RoleAssistant, Content: "The diagnosis is complete."},
		}}}}}
	}
	var output bytes.Buffer
	final, err := tea.NewProgram(
		diagnoseModel{state: state, initial: initial, output: &output},
		tea.WithInput(nil), tea.WithOutput(&output), tea.WithoutRenderer(), tea.WithoutSignalHandler(),
	).Run()
	if err != nil {
		t.Fatal(err)
	}
	result, ok := final.(diagnoseModel)
	if !ok || result.err != nil {
		t.Fatalf("final diagnose model = %#v", final)
	}
	if !strings.Contains(output.String(), "The diagnosis is complete.") {
		t.Fatalf("diagnose output = %q", output.String())
	}
}

func TestDiagnoseModelFailsInsteadOfWaitingForUserInput(t *testing.T) {
	state := newModel(context.Background(), config.Store{}, nil)
	answers := make(chan askToUserOutput, 1)
	initial := func() tea.Msg {
		return agentEventMsg{event: agentEvent{
			question: &askToUserInput{Question: "Provide an internal operational fact"},
			answer:   answers,
		}}
	}
	final, err := tea.NewProgram(
		diagnoseModel{state: state, initial: initial},
		tea.WithInput(nil), tea.WithOutput(&bytes.Buffer{}), tea.WithoutRenderer(), tea.WithoutSignalHandler(),
	).Run()
	if err != nil {
		t.Fatal(err)
	}
	result := final.(diagnoseModel)
	if result.err == nil || !strings.Contains(result.err.Error(), "unexpectedly requested user input") {
		t.Fatalf("diagnose error = %v", result.err)
	}
	answer := <-answers
	if !errors.Is(answer.Err, result.err) {
		t.Fatalf("question answer error = %v, want %v", answer.Err, result.err)
	}
}

func TestDiagnoseIsNotExposedAsALocalSlashCommand(t *testing.T) {
	for _, command := range localSlashCommands {
		if command.name == "/diagnose" {
			t.Fatal("diagnose must remain CLI-only")
		}
	}
}
