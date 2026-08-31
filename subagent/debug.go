package subagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/snowmerak/q/client"
)

const SubmitDebugReportToolName = "submit_debug_report"

const (
	DebugOutcomeSucceeded = "succeeded"
	DebugOutcomeCanceled  = "canceled"
	DebugOutcomeFailed    = "failed"
)

type DebugReport struct {
	Summary        string   `json:"summary"`
	Findings       []string `json:"findings"`
	LikelyCause    string   `json:"likely_cause"`
	Reasoning      []string `json:"reasoning"`
	Confidence     string   `json:"confidence"`
	SuggestedFixes []string `json:"suggested_fixes"`
	Verification   []string `json:"verification"`
	Uncertainties  []string `json:"uncertainties,omitempty"`
}

type DebugWorkflowResult struct {
	Report DebugReport `json:"report"`
}

// DebugExecutionLog is the durable, human-readable audit trail for one
// /debug investigation. It records only visible model output and tool traffic;
// providers' hidden chain-of-thought is neither available nor reconstructed.
type DebugExecutionLog struct {
	RunID         string          `json:"run_id,omitempty"`
	Objective     string          `json:"objective"`
	StartedAt     time.Time       `json:"started_at"`
	CompletedAt   time.Time       `json:"completed_at"`
	Outcome       string          `json:"outcome"`
	Error         string          `json:"error,omitempty"`
	Brief         *GrillBrief     `json:"brief,omitempty"`
	Report        *DebugReport    `json:"report,omitempty"`
	Events        []PlanningEvent `json:"events,omitempty"`
	Truncated     bool            `json:"truncated,omitempty"`
	DroppedEvents int             `json:"dropped_events,omitempty"`
}

type DebugWorkflow struct {
	Griller  GrillerRunner
	Progress ProgressFunc
}

func (w DebugWorkflow) Run(ctx context.Context, task GrillTask) (DebugWorkflowResult, error) {
	reportProgress(w.Progress, ProgressEvent{
		Agent: "debug", Action: ProgressStarted, Detail: "investigating reported issue",
	})
	grillTask := task
	grillTask.ID = planningCycleTaskID(task.ID, "griller", 1)
	if strings.TrimSpace(task.ID) != "" {
		grillTask.ParentID = task.ID
	}
	report, err := w.Griller.RunDebug(ctx, grillTask)
	if err != nil {
		return DebugWorkflowResult{}, err
	}
	reportProgress(w.Progress, ProgressEvent{
		Agent: "debug", Action: ProgressCompleted, Detail: "diagnostic report ready",
	})
	return DebugWorkflowResult{Report: report}, nil
}

func submitDebugReportTool() client.Tool {
	strict := true
	stringsSchema := stringArraySchemaValue()
	return client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
		Name: SubmitDebugReportToolName, Description: "Submit the complete evidence-backed diagnostic report.", Strict: &strict,
		Parameters: map[string]any{
			"type": "object", "properties": map[string]any{
				"summary":         map[string]any{"type": "string"},
				"findings":        stringsSchema,
				"likely_cause":    map[string]any{"type": "string"},
				"reasoning":       stringsSchema,
				"confidence":      map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
				"suggested_fixes": stringsSchema,
				"verification":    stringsSchema,
				"uncertainties":   stringsSchema,
			},
			"required":             []string{"summary", "findings", "likely_cause", "reasoning", "confidence", "suggested_fixes", "verification"},
			"additionalProperties": false,
		},
	}}
}

func parseDebugReport(arguments string) (DebugReport, error) {
	var report DebugReport
	if err := decodeStrict(arguments, &report); err != nil {
		return DebugReport{}, err
	}
	report.Summary = strings.TrimSpace(report.Summary)
	report.Findings = cleanStrings(report.Findings)
	report.LikelyCause = strings.TrimSpace(report.LikelyCause)
	report.Reasoning = cleanStrings(report.Reasoning)
	report.Confidence = strings.ToLower(strings.TrimSpace(report.Confidence))
	report.SuggestedFixes = cleanStrings(report.SuggestedFixes)
	report.Verification = cleanStrings(report.Verification)
	report.Uncertainties = cleanStrings(report.Uncertainties)
	var problems []error
	if report.Summary == "" {
		problems = append(problems, errors.New("summary: must be non-blank"))
	}
	if len(report.Findings) == 0 {
		problems = append(problems, errors.New("findings: include at least one observation"))
	}
	if report.LikelyCause == "" {
		problems = append(problems, errors.New("likely_cause: must be non-blank"))
	}
	if len(report.Reasoning) == 0 {
		problems = append(problems, errors.New("reasoning: explain how the findings support the likely cause"))
	}
	if report.Confidence != "low" && report.Confidence != "medium" && report.Confidence != "high" {
		problems = append(problems, errors.New("confidence: must be low, medium, or high"))
	}
	if len(report.SuggestedFixes) == 0 {
		problems = append(problems, errors.New("suggested_fixes: include at least one evidence-backed next step"))
	}
	if len(report.Verification) == 0 {
		problems = append(problems, errors.New("verification: include at least one confirmation check"))
	}
	if len(problems) > 0 {
		return DebugReport{}, fmt.Errorf("submit_debug_report validation failed; fix all listed fields and resubmit the complete report:\n%w", errors.Join(problems...))
	}
	return report, nil
}

func RenderDebugReport(report DebugReport) string {
	var body strings.Builder
	body.WriteString(report.Summary)
	writePlanList(&body, "Findings", report.Findings)
	if report.LikelyCause != "" {
		body.WriteString("\n\nLikely cause: ")
		body.WriteString(report.LikelyCause)
	}
	writePlanList(&body, "Why this fits", report.Reasoning)
	if report.Confidence != "" {
		body.WriteString("\n\nConfidence: ")
		body.WriteString(report.Confidence)
	}
	writePlanList(&body, "Suggested fix", report.SuggestedFixes)
	writePlanList(&body, "How to verify", report.Verification)
	writePlanList(&body, "Uncertainties", report.Uncertainties)
	return body.String()
}
