package subagent

import (
	"context"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

func TestDebugWorkflowGrillsThenHasPlannerWriteReport(t *testing.T) {
	grillerClient := &fakeScoutClient{responses: []client.Message{{
		Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(SubmitBriefToolName, `{
			"objective":"Investigate denied session replacement",
			"conditions":["The failure occurs during an atomic Windows replacement"],
			"acceptance_criteria":["Explain the likely lock owner and a bounded retry"] ,
			"repository_evidence":["session writes replace session.json with MoveFileExW"]
		}`)},
	}}}
	reporterClient := &fakeScoutClient{responses: []client.Message{{
		Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(SubmitDebugReportToolName, `{
			"summary":"The session replacement is temporarily blocked.",
			"findings":["MoveFileExW returns access denied while replacing session.json"],
			"likely_cause":"Another Windows handle briefly prevents replacement.",
			"reasoning":["The write reaches the atomic replacement step before failing"],
			"confidence":"medium",
			"suggested_fixes":["Retry replacement three times with a short backoff"],
			"verification":["Hold the destination open and verify retry recovery"],
			"uncertainties":["The locking process has not been observed directly"]
		}`)},
	}}}
	tools := &fakeScoutTools{}
	result, err := (DebugWorkflow{
		Griller: GrillerRunner{
			Client: grillerClient, Tools: tools,
			Spec: Spec{Role: config.AgentRoleGriller, Model: "griller-model"},
			Ask: func(context.Context, UserQuestion) (UserAnswer, error) {
				t.Fatal("unexpected user question")
				return UserAnswer{}, nil
			},
		},
		Reporter: DebugReporterRunner{
			Client: reporterClient,
			Spec:   Spec{Role: config.AgentRolePlanner, Model: "planner-model"},
		},
	}).Run(t.Context(), GrillTask{ID: "debug-1", Objective: "Investigate denied session replacement"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Confidence != "medium" || !strings.Contains(result.Report.LikelyCause, "Windows handle") {
		t.Fatalf("result = %#v", result)
	}
	if len(grillerClient.requests) != 1 || len(reporterClient.requests) != 1 {
		t.Fatalf("requests: griller=%d reporter=%d", len(grillerClient.requests), len(reporterClient.requests))
	}
	grillerInstructions := grillerClient.requests[0].Messages[0].Content
	if !strings.Contains(grillerInstructions, "Griller for /debug mode") ||
		!strings.Contains(grillerInstructions, "private organization policy") ||
		!strings.Contains(grillerInstructions, "unpublished internal API") ||
		!strings.Contains(grillerInstructions, "Do not ask the user to choose a technical design") ||
		strings.Contains(grillerInstructions, "Griller for /plan mode") {
		t.Fatalf("debug Griller instructions = %q", grillerInstructions)
	}
	reporterRequest := reporterClient.requests[0]
	if !hasTool(reporterRequest.Tools, SubmitDebugReportToolName) || hasTool(reporterRequest.Tools, SubmitPlanToolName) {
		t.Fatalf("debug reporter tools = %#v", reporterRequest.Tools)
	}
	if !strings.Contains(reporterRequest.Messages[0].Content, "Do not ask the user, modify files, run commands, or implement a fix") {
		t.Fatalf("debug reporter instructions = %q", reporterRequest.Messages[0].Content)
	}
}

func TestDebugGrillerPromptBranchDoesNotChangePlanPrompt(t *testing.T) {
	planPrompt := grillerInstructions()
	debugPrompt := debugGrillerInstructions()
	if !strings.Contains(planPrompt, "Griller for /plan mode") ||
		!strings.Contains(planPrompt, "intent, priorities, constraints, or trade-offs") ||
		strings.Contains(planPrompt, "private organization policy") ||
		strings.Contains(planPrompt, "Griller for /debug mode") {
		t.Fatalf("plan Griller prompt changed: %q", planPrompt)
	}
	if planPrompt == debugPrompt {
		t.Fatal("debug Griller reused the plan prompt")
	}
}

func TestDebugReportValidationAndRendering(t *testing.T) {
	if _, err := parseDebugReport(`{"summary":"Too little"}`); err == nil {
		t.Fatal("incomplete debug report was accepted")
	}
	report, err := parseDebugReport(`{
		"summary":"요약",
		"findings":["관찰"],
		"likely_cause":"추정 원인",
		"reasoning":["근거 연결"],
		"confidence":"HIGH",
		"suggested_fixes":["수정 방향"],
		"verification":["검증 방법"]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	rendered := RenderDebugReport(report)
	for _, expected := range []string{"요약", "Findings", "Likely cause: 추정 원인", "Confidence: high", "Suggested fix", "How to verify"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered report omitted %q: %s", expected, rendered)
		}
	}
}
