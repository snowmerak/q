package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

func TestGrillerReceivesCapturedScoutReport(t *testing.T) {
	scoutClient := &fakeScoutClient{responses: []client.Message{{
		Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(ScoutCompleteToolName, `{
			"outcome":"succeeded",
			"summary":"Located the plan command boundary",
			"findings":[{"path":"app/model.go","symbol":"submitChat","summary":"Slash commands are dispatched before normal chat"}]
		}`)},
	}}}
	grillerClient := &fakeScoutClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(DelegateScoutToolName,
			`{"objective":"Find the slash-command dispatch boundary","completion_criteria":["Identify the responsible function"]}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(SubmitBriefToolName, `{
			"objective":"Add plan mode",
			"conditions":["Reuse the existing chat input"],
			"acceptance_criteria":["The plan is confirmed before execution"],
			"repository_evidence":["app/model.go submitChat dispatches slash commands"]
		}`)}},
	}}
	tools := &fakeScoutTools{}
	runner := GrillerRunner{
		Client: grillerClient, Tools: tools,
		Capture: testInvocationCapture,
		Spec:    Spec{Role: config.AgentRoleGriller, Model: "griller-model"},
		Scout: ScoutRunner{
			Client: scoutClient, Tools: tools,
			Spec: Spec{Role: config.AgentRoleScout, Model: "scout-model"},
		},
		Ask: func(context.Context, UserQuestion) (UserAnswer, error) {
			return UserAnswer{}, errors.New("unexpected user question")
		},
	}
	brief, err := runner.Run(context.Background(), GrillTask{Objective: "Add plan mode"})
	if err != nil {
		t.Fatal(err)
	}
	if brief.Objective != "Add plan mode" || len(grillerClient.requests) != 2 {
		t.Fatalf("brief = %#v, requests = %d", brief, len(grillerClient.requests))
	}
	continuation := grillerClient.requests[1].Messages
	receipt := continuation[len(continuation)-1].Content
	if !strings.Contains(receipt, `"summary":"Located the plan command boundary"`) ||
		!strings.Contains(receipt, `"findings"`) {
		t.Fatalf("Scout report was not returned in the receipt: %s", receipt)
	}
	if !strings.Contains(receipt, `"loom_ref"`) {
		t.Fatalf("Scout report was not captured in Loom: %s", receipt)
	}
}

func TestPlanWorkflowRegrillsAfterUserRevision(t *testing.T) {
	brief := `{
		"objective":"Add plan mode",
		"conditions":["Stop before execution"],
		"acceptance_criteria":["User confirms the plan"]
	}`
	plan := `{
		"outcome":"succeeded",
		"summary":"Introduce a gated plan workflow",
		"conditions":["Stop before execution"],
		"steps":[{"title":"Connect plan mode","description":"Run Griller and Planner before confirmation","target":{"any":[{"all":[{"kind":"paths","paths":["app/model.go"]}]}]}}],
		"verification":["Exercise one complete plan cycle"]
	}`
	grillerClient := &fakeScoutClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(SubmitBriefToolName, brief)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(SubmitBriefToolName, brief)}},
	}}
	plannerClient := &fakeScoutClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(SubmitPlanToolName, plan)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(SubmitPlanToolName, plan)}},
	}}
	answers := []UserAnswer{{Freeform: "Also preserve the previous Grill context"}, {SelectedChoiceID: "approve"}}
	ask := func(context.Context, UserQuestion) (UserAnswer, error) {
		answer := answers[0]
		answers = answers[1:]
		return answer, nil
	}
	tools := &fakeScoutTools{}
	workflow := PlanWorkflow{
		Griller: GrillerRunner{
			Client: grillerClient, Tools: tools, Ask: ask,
			Spec: Spec{Role: config.AgentRoleGriller, Model: "griller-model"},
		},
		Planner: PlannerRunner{
			Client: plannerClient,
			Spec:   Spec{Role: config.AgentRolePlanner, Model: "planner-model"},
		},
		Ask: ask,
	}
	result, err := workflow.Run(context.Background(), GrillTask{Objective: "Add plan mode"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Approved || result.Cycles != 2 || len(grillerClient.requests) != 2 {
		t.Fatalf("result = %#v, Grill requests = %d", result, len(grillerClient.requests))
	}
	secondPrompt := grillerClient.requests[1].Messages[1].Content
	if !strings.Contains(secondPrompt, "Also preserve the previous Grill context") ||
		!strings.Contains(secondPrompt, "Previous Grill brief") ||
		!strings.Contains(secondPrompt, "Previous Planner proposal") {
		t.Fatalf("re-grill did not retain feedback: %s", secondPrompt)
	}
}

func TestGrillerAsksUserAndContinuesSameContext(t *testing.T) {
	grillerClient := &fakeScoutClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(AskToUserToolName,
			`{"question":"Which compatibility target matters?","choices":[{"id":"windows","label":"Windows"}]}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(SubmitBriefToolName, `{
			"objective":"Add plan mode",
			"conditions":["Support Windows terminals"],
			"decisions":["Windows is the compatibility target"],
			"acceptance_criteria":["The plan flow works on Windows"]
		}`)}},
	}}
	runner := GrillerRunner{
		Client: grillerClient, Tools: &fakeScoutTools{},
		Spec: Spec{Role: config.AgentRoleGriller, Model: "griller-model"},
		Ask: func(_ context.Context, question UserQuestion) (UserAnswer, error) {
			if question.Question != "Which compatibility target matters?" {
				t.Fatalf("question = %#v", question)
			}
			return UserAnswer{SelectedChoiceID: "windows"}, nil
		},
	}
	brief, err := runner.Run(context.Background(), GrillTask{Objective: "Add plan mode"})
	if err != nil {
		t.Fatal(err)
	}
	if len(brief.Decisions) != 1 || len(grillerClient.requests) != 2 {
		t.Fatalf("brief = %#v, requests = %d", brief, len(grillerClient.requests))
	}
	continuation := grillerClient.requests[1].Messages
	if !strings.Contains(continuation[len(continuation)-1].Content, `"selected_choice_id":"windows"`) {
		t.Fatalf("Griller did not receive the user answer: %#v", continuation)
	}
}

func TestPlanningValidationRequiresExecutableContract(t *testing.T) {
	if _, err := parseGrillBrief(`{"objective":"Plan","conditions":[],"acceptance_criteria":[]}`); err == nil {
		t.Fatal("empty Grill contract was accepted")
	}
	if _, err := parsePlanProposal(`{"outcome":"succeeded","summary":"Plan"}`); err == nil {
		t.Fatal("successful plan without conditions, steps, and verification was accepted")
	}
}

func TestGrillerQuestionChoicesAreNonExhaustive(t *testing.T) {
	if prompt := grillerInstructions(); !strings.Contains(prompt, "non-exhaustive") ||
		!strings.Contains(prompt, "free-form answer") || !strings.Contains(prompt, "not treat every choice as an action") {
		t.Fatalf("Griller prompt does not preserve free-form answers:\n%s", prompt)
	}
	if description := askUserTool().Function.Description; !strings.Contains(description, "non-exhaustive") ||
		!strings.Contains(description, "free-form") {
		t.Fatalf("ask_to_user description = %q", description)
	}
}

func TestGrillerCallsExternalSearchAndReceivesCapturedResult(t *testing.T) {
	grillerClient := &fakeScoutClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(ExternalSearchToolName,
			`{"query":"current ACP session lifecycle","completion_criteria":["cite the specification"]}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(SubmitBriefToolName, `{
			"objective":"Use ACP search",
			"conditions":["Delete the isolated session"],
			"acceptance_criteria":["Search evidence reaches Griller"]
		}`)}},
	}}
	var received ExternalSearchInput
	tools := testExternalSearchRuntime(t, &received, ExternalSearchResult{
		Agent: "codex", Summary: "ACP session/delete is advertised", Sources: []string{"https://agentclientprotocol.com"},
	})
	runner := GrillerRunner{
		Client: grillerClient, Tools: tools, Capture: testInvocationCapture,
		Spec: Spec{Role: config.AgentRoleGriller, Model: "griller-model"},
		Ask: func(context.Context, UserQuestion) (UserAnswer, error) {
			return UserAnswer{}, errors.New("unexpected user question")
		},
	}
	brief, err := runner.Run(t.Context(), GrillTask{Objective: "Use ACP search"})
	if err != nil {
		t.Fatal(err)
	}
	if received.Query != "current ACP session lifecycle" || brief.Objective != "Use ACP search" || len(grillerClient.requests) != 2 {
		t.Fatalf("input=%#v brief=%#v", received, brief)
	}
	if !hasTool(grillerClient.requests[0].Tools, ExternalSearchToolName) ||
		!strings.Contains(grillerClient.requests[1].Messages[len(grillerClient.requests[1].Messages)-1].Content, "session/delete") ||
		!strings.Contains(grillerClient.requests[1].Messages[len(grillerClient.requests[1].Messages)-1].Content, "loom_ref") {
		t.Fatalf("requests = %#v", grillerClient.requests)
	}
}

func TestPlannerCallsExternalSearchAndReceivesCapturedResult(t *testing.T) {
	plannerClient := &fakeScoutClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(ExternalSearchToolName,
			`{"query":"current ACP plan support","completion_criteria":["cite the specification"]}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(SubmitPlanToolName, `{
			"outcome":"succeeded",
			"summary":"Use ACP plan evidence",
			"conditions":["Preserve ACP compatibility"],
			"steps":[{"title":"Use ACP evidence","description":"Apply the externally verified behavior","target":{"any":[{"all":[{"kind":"paths","paths":["app/acp.go"]}]}]}}],
			"verification":["Run ACP tests"]
		}`)}},
	}}
	var received ExternalSearchInput
	tools := testExternalSearchRuntime(t, &received, ExternalSearchResult{
		Agent: "grok", Summary: "ACP plan updates are supported", Sources: []string{"https://agentclientprotocol.com"},
	})
	runner := PlannerRunner{
		Client: plannerClient,
		Spec:   Spec{Role: config.AgentRolePlanner, Model: "planner-model"},
		Tools:  tools,
	}
	proposal, err := runner.Run(t.Context(), GrillBrief{
		Objective: "Use ACP plan evidence", Conditions: []string{"Preserve ACP compatibility"},
		AcceptanceCriteria: []string{"The plan cites verified behavior"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if received.Query != "current ACP plan support" || proposal.Summary != "Use ACP plan evidence" || len(plannerClient.requests) != 2 {
		t.Fatalf("input=%#v proposal=%#v", received, proposal)
	}
	if !hasTool(plannerClient.requests[0].Tools, ExternalSearchToolName) ||
		!strings.Contains(plannerClient.requests[1].Messages[len(plannerClient.requests[1].Messages)-1].Content, "plan updates") ||
		!strings.Contains(plannerClient.requests[1].Messages[len(plannerClient.requests[1].Messages)-1].Content, "loom_ref") {
		t.Fatalf("requests = %#v", plannerClient.requests)
	}
}

func testExternalSearchRuntime(
	t *testing.T,
	received *ExternalSearchInput,
	result ExternalSearchResult,
) ToolRuntime {
	t.Helper()
	runtime, err := NewInvocationRuntime(&fakeScoutTools{}, testInvocationCapture, Invocation{
		Tool:   ExternalSearchTool(),
		Source: InvocationSource{Protocol: "acp", Name: result.Agent, Kind: "agent-result"},
		Handler: func(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
			input, err := ParseExternalSearchInput(call.Function.Arguments)
			if err != nil {
				return client.ToolResult{}, err
			}
			*received = input
			return jsonToolResult(result), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func testInvocationCapture(
	_ context.Context,
	_ InvocationSource,
	_ client.ToolCall,
	result client.ToolResult,
) (client.ToolResult, error) {
	var value any
	if err := json.Unmarshal([]byte(result.Content), &value); err != nil {
		value = result.Content
	}
	body, err := json.Marshal(map[string]any{
		"loom_ref": "loom://0123456789abcdef0123456789abcdef", "stored": true, "result": value,
	})
	if err != nil {
		return client.ToolResult{}, err
	}
	return client.ToolResult{Content: string(body), IsError: result.IsError}, nil
}
