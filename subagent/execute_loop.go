package subagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const defaultTaskAttempts = 3

type ExecutionPhase string

const (
	ExecutionPhaseTarget        ExecutionPhase = "target"
	ExecutionPhaseCoderPending  ExecutionPhase = "coder_pending"
	ExecutionPhaseCoderRunning  ExecutionPhase = "coder_running"
	ExecutionPhaseReviewPending ExecutionPhase = "review_pending"
	ExecutionPhaseCompleted     ExecutionPhase = "completed"
)

const interruptedCoderFeedback = "A previous Coder attempt was interrupted after it may have partially changed the workspace. Inspect the current workspace and existing changes first, then continue or repair the task without assuming the interrupted attempt did nothing."

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
	Checkpoint  func(context.Context, ExecutionCheckpoint) error
}

type PlanExecutionResult struct {
	Plan           PlanProposal          `json:"plan"`
	CompletedTasks int                   `json:"completed_tasks"`
	Attempts       int                   `json:"attempts"`
	Tasks          []TaskExecutionResult `json:"tasks,omitempty"`
}

type TaskExecutionResult struct {
	TaskIndex int         `json:"task_index"`
	Title     string      `json:"title"`
	Attempts  int         `json:"attempts"`
	Result    CoderResult `json:"result"`
}

// ExecutionCheckpoint is the durable state machine for an approved plan.
// CoderRunning is deliberately distinct from CoderPending: after a process
// interruption it must be converted into a new recovery attempt instead of
// blindly replaying a possibly side-effecting Coder invocation.
type ExecutionCheckpoint struct {
	ExecutionID    string                `json:"execution_id,omitempty"`
	RunID          string                `json:"run_id,omitempty"`
	Objective      string                `json:"objective,omitempty"`
	Phase          ExecutionPhase        `json:"phase"`
	Plan           PlanProposal          `json:"plan"`
	TaskIndex      int                   `json:"task_index"`
	Attempt        int                   `json:"attempt"`
	Targets        []string              `json:"resolved_targets,omitempty"`
	Feedback       string                `json:"feedback"`
	PendingResult  *CoderResult          `json:"pending_result,omitempty"`
	CompletedTasks int                   `json:"completed_tasks"`
	Attempts       int                   `json:"attempts"`
	TaskAttempts   int                   `json:"task_attempts"`
	ResumeCount    int                   `json:"resume_count"`
	Tasks          []TaskExecutionResult `json:"tasks,omitempty"`
}

func NewExecutionCheckpoint(plan PlanProposal) ExecutionCheckpoint {
	return ExecutionCheckpoint{Phase: ExecutionPhaseTarget, Plan: plan, Attempt: 1}
}

// PrepareExecutionResume makes an interrupted Coder invocation safe to
// continue. Other phases are already idempotent: target resolution is read-only
// and Planner review has no workspace mutation tools.
func PrepareExecutionResume(checkpoint ExecutionCheckpoint) (ExecutionCheckpoint, error) {
	if err := ValidateExecutionCheckpoint(checkpoint); err != nil {
		return ExecutionCheckpoint{}, err
	}
	checkpoint.ResumeCount++
	if checkpoint.Phase == ExecutionPhaseCoderRunning {
		checkpoint.Phase = ExecutionPhaseCoderPending
		checkpoint.Attempt++
		checkpoint.PendingResult = nil
		if strings.TrimSpace(checkpoint.Feedback) == "" {
			checkpoint.Feedback = interruptedCoderFeedback
		} else {
			checkpoint.Feedback = strings.TrimSpace(checkpoint.Feedback) + "\n\n" + interruptedCoderFeedback
		}
	}
	return checkpoint, nil
}

func ValidateExecutionCheckpoint(checkpoint ExecutionCheckpoint) error {
	if len(checkpoint.Plan.Steps) == 0 {
		return errors.New("subagent: execution checkpoint has no plan tasks")
	}
	for index := range checkpoint.Plan.Steps {
		if err := validateTargetCondition(&checkpoint.Plan.Steps[index].Target); err != nil {
			return fmt.Errorf("subagent: execution checkpoint task %d target: %w", index+1, err)
		}
	}
	if checkpoint.TaskIndex < 0 || checkpoint.TaskIndex > len(checkpoint.Plan.Steps) {
		return fmt.Errorf("subagent: execution checkpoint task index %d is out of range", checkpoint.TaskIndex)
	}
	if checkpoint.Attempt < 1 || checkpoint.Attempts < 0 || checkpoint.TaskAttempts < 0 || checkpoint.ResumeCount < 0 {
		return errors.New("subagent: execution checkpoint counters are invalid")
	}
	if checkpoint.CompletedTasks < 0 || checkpoint.CompletedTasks > len(checkpoint.Plan.Steps) ||
		checkpoint.CompletedTasks != len(checkpoint.Tasks) || checkpoint.TaskIndex < checkpoint.CompletedTasks {
		return errors.New("subagent: execution checkpoint completed task projection is invalid")
	}
	switch checkpoint.Phase {
	case ExecutionPhaseTarget:
		if checkpoint.TaskIndex >= len(checkpoint.Plan.Steps) {
			return errors.New("subagent: target checkpoint is past the final task")
		}
	case ExecutionPhaseCoderPending, ExecutionPhaseCoderRunning:
		if checkpoint.TaskIndex >= len(checkpoint.Plan.Steps) || len(cleanStrings(checkpoint.Targets)) == 0 {
			return errors.New("subagent: Coder checkpoint requires a current task and resolved targets")
		}
		if err := validateResolvedTargets(checkpoint.Targets); err != nil {
			return err
		}
	case ExecutionPhaseReviewPending:
		if checkpoint.TaskIndex >= len(checkpoint.Plan.Steps) || len(cleanStrings(checkpoint.Targets)) == 0 || checkpoint.PendingResult == nil {
			return errors.New("subagent: review checkpoint requires targets and a Coder result")
		}
		if err := validateResolvedTargets(checkpoint.Targets); err != nil {
			return err
		}
		if err := validateCoderResult(*checkpoint.PendingResult); err != nil {
			return fmt.Errorf("subagent: execution checkpoint Coder result: %w", err)
		}
		if checkpoint.TaskAttempts < 1 {
			return errors.New("subagent: review checkpoint requires a completed Coder attempt")
		}
	case ExecutionPhaseCompleted:
		if checkpoint.TaskIndex != len(checkpoint.Plan.Steps) || checkpoint.CompletedTasks != len(checkpoint.Plan.Steps) {
			return errors.New("subagent: completed checkpoint does not contain every task")
		}
	default:
		return fmt.Errorf("subagent: unsupported execution checkpoint phase %q", checkpoint.Phase)
	}
	return nil
}

func validateResolvedTargets(targets []string) error {
	for _, target := range cleanStrings(targets) {
		if !workspaceRelativePath(target) {
			return fmt.Errorf("subagent: execution checkpoint target %q must stay workspace-relative", target)
		}
	}
	return nil
}

func (l ExecutionLoop) Run(ctx context.Context, plan PlanProposal) (PlanExecutionResult, error) {
	return l.RunFrom(ctx, NewExecutionCheckpoint(plan))
}

func (l ExecutionLoop) RunFrom(ctx context.Context, checkpoint ExecutionCheckpoint) (PlanExecutionResult, error) {
	if ctx == nil {
		return PlanExecutionResult{}, errors.New("subagent: execution context is nil")
	}
	if l.Coder == nil || l.Review == nil {
		return PlanExecutionResult{}, errors.New("subagent: execution requires Coder and Planner review functions")
	}
	if err := ValidateExecutionCheckpoint(checkpoint); err != nil {
		return PlanExecutionResult{}, err
	}
	maximumAttempts := l.MaxAttempts
	if maximumAttempts <= 0 {
		maximumAttempts = defaultTaskAttempts
	}
	if checkpoint.Phase == ExecutionPhaseCoderRunning {
		return checkpoint.result(), errors.New("subagent: interrupted Coder checkpoint requires PrepareExecutionResume")
	}
	for checkpoint.Phase != ExecutionPhaseCompleted {
		taskIndex := checkpoint.TaskIndex
		switch checkpoint.Phase {
		case ExecutionPhaseTarget:
			reportProgress(l.Progress, ProgressEvent{
				Agent: "executor", Action: ProgressStarted,
				Detail: fmt.Sprintf("resolving task %d/%d target", taskIndex+1, len(checkpoint.Plan.Steps)),
			})
			targets, err := l.Resolver.Resolve(ctx, checkpoint.Plan.Steps[taskIndex].Target)
			if err != nil {
				return checkpoint.result(), fmt.Errorf("subagent: resolve task %d target: %w", taskIndex+1, err)
			}
			checkpoint.Targets = append([]string(nil), targets...)
			checkpoint.Feedback = ""
			checkpoint.PendingResult = nil
			checkpoint.Attempt = 1
			checkpoint.TaskAttempts = 0
			checkpoint.Phase = ExecutionPhaseCoderPending
			if err := l.persist(ctx, checkpoint); err != nil {
				return checkpoint.result(), err
			}
			reportProgress(l.Progress, ProgressEvent{
				Agent: "executor", Action: ProgressCompleted,
				Detail: fmt.Sprintf("task %d target · %d files", taskIndex+1, len(targets)),
			})
		case ExecutionPhaseCoderPending:
			if checkpoint.TaskAttempts >= maximumAttempts {
				return checkpoint.result(), fmt.Errorf("subagent: task %d exhausted %d attempts", taskIndex+1, maximumAttempts)
			}
			reportProgress(l.Progress, ProgressEvent{
				Agent: "coder", Action: ProgressStarted,
				Detail: fmt.Sprintf("task %d/%d · attempt %d", taskIndex+1, len(checkpoint.Plan.Steps), checkpoint.Attempt),
			})
			checkpoint.Phase = ExecutionPhaseCoderRunning
			if err := l.persist(ctx, checkpoint); err != nil {
				return checkpoint.result(), err
			}
			coderResult, err := l.Coder(ctx, CoderAttempt{
				Plan: checkpoint.Plan, TaskIndex: taskIndex, Attempt: checkpoint.Attempt,
				Targets: append([]string(nil), checkpoint.Targets...), Feedback: checkpoint.Feedback,
			})
			if err != nil {
				reportProgress(l.Progress, ProgressEvent{Agent: "coder", Action: ProgressFailed, Detail: err.Error()})
				return checkpoint.result(), fmt.Errorf("subagent: Coder task %d attempt %d: %w", taskIndex+1, checkpoint.Attempt, err)
			}
			checkpoint.Attempts++
			checkpoint.TaskAttempts++
			checkpoint.PendingResult = &coderResult
			checkpoint.Phase = ExecutionPhaseReviewPending
			if err := l.persist(ctx, checkpoint); err != nil {
				return checkpoint.result(), err
			}
			reportProgress(l.Progress, ProgressEvent{Agent: "coder", Action: ProgressCompleted, Detail: coderResult.Summary})
		case ExecutionPhaseReviewPending:
			review, err := l.Review(ctx, TaskReviewRequest{
				Plan: checkpoint.Plan, TaskIndex: taskIndex, Attempt: checkpoint.Attempt,
				Targets: append([]string(nil), checkpoint.Targets...), Result: *checkpoint.PendingResult,
			})
			if err != nil {
				return checkpoint.result(), fmt.Errorf("subagent: review task %d attempt %d: %w", taskIndex+1, checkpoint.Attempt, err)
			}
			if err := ApplyTaskReview(&checkpoint.Plan, review); err != nil {
				return checkpoint.result(), err
			}
			if review.Decision == "next" {
				checkpoint.CompletedTasks++
				checkpoint.Tasks = append(checkpoint.Tasks, TaskExecutionResult{
					TaskIndex: taskIndex, Title: checkpoint.Plan.Steps[taskIndex].Title,
					Attempts: checkpoint.TaskAttempts, Result: *checkpoint.PendingResult,
				})
				checkpoint.TaskIndex++
				checkpoint.Targets = nil
				checkpoint.Feedback = ""
				checkpoint.PendingResult = nil
				checkpoint.Attempt = 1
				checkpoint.TaskAttempts = 0
				if checkpoint.TaskIndex == len(checkpoint.Plan.Steps) {
					checkpoint.Phase = ExecutionPhaseCompleted
				} else {
					checkpoint.Phase = ExecutionPhaseTarget
				}
			} else {
				checkpoint.Feedback = review.Feedback
				checkpoint.PendingResult = nil
				checkpoint.Attempt++
				checkpoint.Phase = ExecutionPhaseCoderPending
			}
			if err := l.persist(ctx, checkpoint); err != nil {
				return checkpoint.result(), err
			}
		}
	}
	return checkpoint.result(), nil
}

func (l ExecutionLoop) persist(ctx context.Context, checkpoint ExecutionCheckpoint) error {
	if l.Checkpoint == nil {
		return nil
	}
	if err := l.Checkpoint(ctx, checkpoint); err != nil {
		return fmt.Errorf("subagent: persist execution checkpoint: %w", err)
	}
	return nil
}

func (c ExecutionCheckpoint) result() PlanExecutionResult {
	return PlanExecutionResult{
		Plan: c.Plan, CompletedTasks: c.CompletedTasks, Attempts: c.Attempts,
		Tasks: append([]TaskExecutionResult(nil), c.Tasks...),
	}
}

func RenderPlanExecutionResult(result PlanExecutionResult) string {
	var body strings.Builder
	body.WriteString("Plan executed successfully.")
	body.WriteString(fmt.Sprintf("\n\nCompleted %d task(s) in %d Coder attempt(s).", result.CompletedTasks, result.Attempts))
	for _, task := range result.Tasks {
		body.WriteString(fmt.Sprintf("\n\n%d. %s", task.TaskIndex+1, task.Title))
		body.WriteString(fmt.Sprintf("\n   %s", task.Result.Summary))
		if task.Attempts > 1 {
			body.WriteString(fmt.Sprintf("\n   attempts: %d", task.Attempts))
		}
		for _, verification := range task.Result.Verification {
			body.WriteString("\n   verification: ")
			body.WriteString(verification)
		}
	}
	return body.String()
}
