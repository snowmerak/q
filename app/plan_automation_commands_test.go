package app

import (
	"context"
	"strings"
	"testing"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

func TestParsePlanAutomationCommands(t *testing.T) {
	tests := []struct {
		input  string
		target planAutomationCommandTarget
		action planAutomationCommandAction
		valid  bool
	}{
		{input: "/auto-approve", target: planAutomationAutoApprove, action: planAutomationStatus, valid: true},
		{input: "/auto-approve on", target: planAutomationAutoApprove, action: planAutomationEnable, valid: true},
		{input: "/auto-resolve off", target: planAutomationAutoResolve, action: planAutomationDisable, valid: true},
		{input: "/autonomous status", target: planAutomationAutonomous, action: planAutomationStatus, valid: true},
		{input: "/autonomous later", target: planAutomationAutonomous},
	}
	for _, test := range tests {
		command, handled := parsePlanAutomationCommand(test.input)
		if !handled || command.target != test.target || command.action != test.action || command.valid != test.valid {
			t.Fatalf("parse %q = %#v, handled=%v", test.input, command, handled)
		}
	}
	if _, handled := parsePlanAutomationCommand("/auto-resolver on"); handled {
		t.Fatal("unrelated command was handled as plan automation")
	}
}

func TestTUIPlanAutomationCommandsPersistConfig(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := config.Default()
	value.Provider.Model = "test-model"
	configuredClient := &fakeClient{}
	m := newModel(context.Background(), store, nil)
	m.enterChat(value, configuredClient)

	for _, step := range []struct {
		command string
		want    config.PlanConfig
		status  string
	}{
		{command: "/auto-approve on", want: config.PlanConfig{AutoApprove: true}, status: "Auto-approve saved on."},
		{command: "/auto-resolve on", want: config.PlanConfig{AutoApprove: true, AutoResolve: true}, status: "Auto-resolve saved on."},
		{command: "/autonomous off", want: config.PlanConfig{}, status: "Plan automation saved: auto-approve off · auto-resolve off."},
	} {
		m.input.SetValue(step.command)
		updated, _ := m.submitChat()
		m = updated.(model)
		if m.config.Plan != step.want || m.status != step.status {
			t.Fatalf("%s: plan=%#v status=%q", step.command, m.config.Plan, m.status)
		}
		loaded, err := store.Load()
		if err != nil || loaded.Plan != step.want {
			t.Fatalf("%s: stored plan=%#v err=%v", step.command, loaded.Plan, err)
		}
	}
	if len(configuredClient.requests) != 0 {
		t.Fatalf("plan setting commands reached the model: %d requests", len(configuredClient.requests))
	}
}

func TestACPPlanAutomationCommandsPersistAndReportProcessOverrides(t *testing.T) {
	agent, workspaceStore, connection := testACPAgent(t, &fakeClient{}, &fakeAgentTools{})
	agent.planOverrides.AutoApprove = BooleanOverride{Set: true, Value: false}
	first := openTestACPSession(t, agent, workspaceStore.Root)
	second := openTestACPSession(t, agent, workspaceStore.Root)

	response, err := agent.Prompt(t.Context(), acp.PromptRequest{
		SessionId: first,
		Prompt:    []acp.ContentBlock{acp.TextBlock("/autonomous on")},
	})
	if err != nil || response.StopReason != acp.StopReasonEndTurn {
		t.Fatalf("autonomous command response=%#v err=%v", response, err)
	}
	loaded, err := agent.state.store.Load()
	if err != nil || loaded.Plan != (config.PlanConfig{AutoApprove: true, AutoResolve: true}) {
		t.Fatalf("stored plan=%#v err=%v", loaded.Plan, err)
	}
	effective := activeACPRuntime(t, agent, second).effectivePlanConfig()
	if effective.AutoApprove || !effective.AutoResolve {
		t.Fatalf("effective plan in second session=%#v", effective)
	}

	var output string
	for _, notification := range connection.snapshot() {
		if update := notification.Update.AgentMessageChunk; update != nil && update.Content.Text != nil {
			output += update.Content.Text.Text
		}
	}
	if !strings.Contains(output, "configured auto-approve on · auto-resolve on") ||
		!strings.Contains(output, "effective auto-approve off · auto-resolve on for this process") {
		t.Fatalf("ACP automation status omitted config/override distinction: %q", output)
	}
}

func TestInvalidPlanAutomationCommandReturnsUsage(t *testing.T) {
	value := config.Default()
	value.Provider.Model = "test-model"
	m := newModel(context.Background(), config.Store{Dir: t.TempDir()}, nil)
	m.enterChat(value, &fakeClient{})
	m.input.SetValue("/auto-resolve maybe")
	updated, _ := m.submitChat()
	m = updated.(model)
	if m.status != "Usage: /auto-resolve [on|off|status]" {
		t.Fatalf("invalid command status=%q", m.status)
	}
}
