package task

import (
	"context"
	"strings"

	sdk "github.com/snowmerak/decisions"
	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/llm/decisions"
	"github.com/snowmerak/q/v2/pkg/memory"
)

// EffectiveMode resolves the active approval mode with precedence:
// per-tool override > room override > character override > global default.
func EffectiveMode(
	globalMode domain.ApprovalMode,
	roomOverride *domain.ApprovalMode,
	charOverride *domain.ApprovalMode,
	toolOverrides map[string]domain.ApprovalMode,
	toolName string,
) domain.ApprovalMode {
	if mode, ok := toolOverrides[toolName]; ok && mode.Valid() {
		return mode
	}
	if roomOverride != nil && roomOverride.Valid() {
		return *roomOverride
	}
	if charOverride != nil && charOverride.Valid() {
		return *charOverride
	}
	if globalMode.Valid() {
		return globalMode
	}
	return domain.ApprovalAutoDetection
}

// EvaluateApproval determines whether invoking spec with argsJSON requires
// explicit user confirmation under mode. In auto-detection mode, any Decisions
// error fails closed by requiring explicit user approval.
func EvaluateApproval(
	ctx context.Context,
	dec memory.DecisionEvaluator,
	decisionModel string,
	mode domain.ApprovalMode,
	spec ToolSpec,
	argsJSON string,
	userRequest string,
) (requiresApproval bool, reason string) {
	if spec.SkipApprovalGate {
		return false, "tool skips approval gate"
	}
	switch mode {
	case domain.ApprovalBypass:
		return false, "mode=bypass"
	case domain.ApprovalRequest:
		return true, "mode=request"
	case domain.ApprovalAutoDetection:
		if dec == nil || strings.TrimSpace(decisionModel) == "" {
			return true, "fail_closed: decisions model is not configured"
		}
		summaryArgs := strings.TrimSpace(argsJSON)
		if len(summaryArgs) > 1200 {
			summaryArgs = summaryArgs[:1200] + "...(truncated)"
		}
		resp, err := dec.Evaluate(ctx, decisionModel, sdk.Input{
			State: map[string]any{
				"tool_name":    spec.Name,
				"tool_source":  string(spec.Source),
				"risk_class":   string(spec.Risk),
				"args_json":    summaryArgs,
				"user_request": userRequest,
			},
		}, sdk.Questions{
			"requires_approval": sdk.NoulQuestion{
				Instructions: "Should this tool call require explicit user confirmation before executing? Return false for low-risk read-only queries clearly matching user_request (reading files within workspace, searching archive/knowledge/skills, inspecting artifacts). Return true for file writes/deletes, shell execution, external MCP/browser actions, or anything ambiguous/destructive.",
			},
		})
		if err != nil {
			return true, "fail_closed: " + err.Error()
		}
		req, posErr := decisions.Positive(resp, "requires_approval")
		if posErr != nil {
			return true, "fail_closed: " + posErr.Error()
		}
		if req {
			return true, "auto-detection: requires user approval"
		}
		return false, "auto-detection: approved"
	default:
		return true, "fail_closed: unknown approval mode"
	}
}
