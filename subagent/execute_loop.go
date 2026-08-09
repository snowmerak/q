package subagent

import (
	"context"
	"errors"
	"fmt"
)

const defaultTaskAttempts = 3

type CoderAttempt struct {
	Plan      PlanProposal `json:"plan"`
	TaskIndex int          `json:"task_index"`
	Attempt   int          `json:"attempt"`
	Targets   []string     `json:"resolved_targets"`
	Feedback  string       `json:"feedback"`
}

type CoderRunFunc func(context.Context, CoderAttempt) (CoderResult, error)
type PlannerReviewFunc func(context.Context, TaskReviewRequest) (TaskReview, error)

type ExecutionLoop struct {
	Resolver    TargetResolver
	Coder       CoderRunFunc
	Review      PlannerReviewFunc
	MaxAttempts int
	Progress    ProgressFunc
}

type PlanExecutionResult struct {
	Plan           PlanProposal `json:"plan"`
	CompletedTasks int          `json:"completed_tasks"`
	Attempts       int          `json:"attempts"`
}

func (l ExecutionLoop) Run(ctx context.Context, plan PlanProposal) (PlanExecutionResult, error) {
	if ctx == nil {
		return PlanExecutionResult{}, errors.New("subagent: execution context is nil")
	}
	if l.Coder == nil || l.Review == nil {
		return PlanExecutionResult{}, errors.New("subagent: execution requires Coder and Planner review functions")
	}
	if len(plan.Steps) == 0 {
		return PlanExecutionResult{}, errors.New("subagent: execution plan has no tasks")
	}
	maximumAttempts := l.MaxAttempts
	if maximumAttempts <= 0 {
		maximumAttempts = defaultTaskAttempts
	}
	result := PlanExecutionResult{Plan: plan}
	for taskIndex := range result.Plan.Steps {
		targets, err := l.Resolver.Resolve(ctx, result.Plan.Steps[taskIndex].Target)
		if err != nil {
			return result, fmt.Errorf("subagent: resolve task %d target: %w", taskIndex+1, err)
		}
		feedback := ""
		advanced := false
		for attempt := 1; attempt <= maximumAttempts; attempt++ {
			reportProgress(l.Progress, ProgressEvent{
				Agent: "coder", Action: ProgressStarted,
				Detail: fmt.Sprintf("task %d/%d · attempt %d", taskIndex+1, len(result.Plan.Steps), attempt),
			})
			coderResult, err := l.Coder(ctx, CoderAttempt{
				Plan: result.Plan, TaskIndex: taskIndex, Attempt: attempt,
				Targets: append([]string(nil), targets...), Feedback: feedback,
			})
			result.Attempts++
			if err != nil {
				reportProgress(l.Progress, ProgressEvent{Agent: "coder", Action: ProgressFailed, Detail: err.Error()})
				return result, fmt.Errorf("subagent: Coder task %d attempt %d: %w", taskIndex+1, attempt, err)
			}
			reportProgress(l.Progress, ProgressEvent{Agent: "coder", Action: ProgressCompleted, Detail: coderResult.Summary})
			review, err := l.Review(ctx, TaskReviewRequest{
				Plan: result.Plan, TaskIndex: taskIndex, Attempt: attempt, Result: coderResult,
			})
			if err != nil {
				return result, fmt.Errorf("subagent: review task %d attempt %d: %w", taskIndex+1, attempt, err)
			}
			if err := ApplyTaskReview(&result.Plan, review); err != nil {
				return result, err
			}
			if review.Decision == "next" {
				result.CompletedTasks++
				advanced = true
				break
			}
			feedback = review.Feedback
		}
		if !advanced {
			return result, fmt.Errorf("subagent: task %d exhausted %d attempts", taskIndex+1, maximumAttempts)
		}
	}
	return result, nil
}
