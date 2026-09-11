package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
)

func configuredPlanExecutors(value config.Config) []string {
	result := []string{subagent.PlanExecutorCoder}
	if _, _, available := value.ExternalAgentConnection(config.AgentRoleExternalWebTester); available {
		result = append(result, subagent.PlanExecutorExternalWebTester)
	}
	return result
}

func configuredExternalWebTesterTaskRunner(
	value config.Config,
	root string,
	base agentToolRuntime,
) (subagent.TaskRunFunc, bool, error) {
	invocation, configured := configuredExternalWebTesterInvocation(value, root)
	if !configured {
		return nil, false, nil
	}
	capture := configuredInvocationCapture(base)
	if capture == nil {
		return nil, false, errors.New("plan: external web tester requires Loom capture")
	}
	return externalWebTesterTaskRunner(invocation, capture), true, nil
}

func externalWebTesterTaskRunner(
	invocation subagent.Invocation,
	capture subagent.InvocationCaptureFunc,
) subagent.TaskRunFunc {
	return func(ctx context.Context, attempt subagent.TaskAttempt) (subagent.TaskResult, error) {
		step := attempt.Plan.Steps[attempt.TaskIndex]
		input := subagent.ExternalWebTesterInput{
			Request: strings.TrimSpace(step.Title + "\n\n" + step.Description),
			Context: append([]string{
				"Plan objective: " + attempt.Plan.Summary,
				"Resolved workspace targets: " + strings.Join(attempt.Targets, ", "),
			}, attempt.Plan.Facts...),
			CompletionCriteria: append(append([]string(nil), step.Verification...), attempt.Plan.Verification...),
		}
		if feedback := strings.TrimSpace(attempt.Feedback); feedback != "" {
			input.Context = append(input.Context, "Planner retry feedback: "+feedback)
		}
		arguments, err := json.Marshal(input)
		if err != nil {
			return subagent.TaskResult{}, fmt.Errorf("plan: encode external web tester task: %w", err)
		}
		call := client.ToolCall{
			ID:   fmt.Sprintf("plan-web-tester-%d-%d", attempt.TaskIndex+1, attempt.Attempt),
			Type: client.ToolTypeFunction,
			Function: client.FunctionCall{
				Name: subagent.ExternalWebTesterToolName, Arguments: string(arguments),
			},
		}
		raw, handlerErr := invocation.Handler(ctx, call)
		if handlerErr != nil {
			body, marshalErr := json.Marshal(map[string]string{"error": handlerErr.Error()})
			if marshalErr != nil {
				return subagent.TaskResult{}, errors.Join(handlerErr, marshalErr)
			}
			raw = client.ToolResult{Content: string(body), IsError: true}
		}
		receipt, err := capture(ctx, invocation.Source, call, raw)
		if err != nil {
			return subagent.TaskResult{}, errors.Join(handlerErr, err)
		}
		if handlerErr != nil {
			return subagent.TaskResult{}, handlerErr
		}
		if raw.IsError {
			return subagent.TaskResult{}, errors.New("external web tester returned an error result")
		}
		report, err := subagent.ParseExternalWebTesterResult(raw.Content)
		if err != nil {
			return subagent.TaskResult{}, err
		}
		result := subagent.TaskResult{
			Executor: subagent.PlanExecutorExternalWebTester,
			Outcome:  report.Outcome, Summary: report.Summary, Findings: report.Findings,
			Verification: report.Verification, Artifacts: report.Artifacts, Blocker: report.Blocker,
		}
		var captured struct {
			LoomRef string `json:"loom_ref"`
		}
		if json.Unmarshal([]byte(receipt.Content), &captured) == nil && strings.TrimSpace(captured.LoomRef) != "" {
			result.Evidence = []subagent.CoderEvidence{{
				Tool: subagent.ExternalWebTesterToolName, LoomRef: captured.LoomRef, IsError: raw.IsError,
			}}
		}
		return result, nil
	}
}
