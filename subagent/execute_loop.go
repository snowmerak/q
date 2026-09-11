package subagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const defaultTaskAttempts = 30

type ExecutionPhase string

const (
	ExecutionPhaseTarget          ExecutionPhase = "target"
	ExecutionPhaseExecutorPending ExecutionPhase = "executor_pending"
	ExecutionPhaseExecutorRunning ExecutionPhase = "executor_running"
	ExecutionPhaseReviewPending   ExecutionPhase = "review_pending"
	ExecutionPhaseCompleted       ExecutionPhase = "completed"

	// Deprecated names remain aliases so callers compiled against the Coder-only
	// execution loop move to the generalized phases without a source break.
	ExecutionPhaseCoderPending = ExecutionPhaseExecutorPending
	ExecutionPhaseCoderRunning = ExecutionPhaseExecutorRunning
)

const (
	interruptedCoderFeedback    = "A previous Coder attempt was interrupted after it may have partially changed the workspace. Inspect the current workspace and existing changes first, then continue or repair the task without assuming the interrupted attempt did nothing."
	interruptedExecutorFeedback = "A previous external Web Tester attempt was interrupted after it may have partially changed external or workspace state. Inspect the current state first, then continue, repair, or retest without assuming the interrupted attempt did nothing."
)

type TaskAttempt struct {
	Plan      PlanProposal `json:"plan"`
	TaskIndex int          `json:"task_index"`
	Attempt   int          `json:"attempt"`
	Executor  string       `json:"executor"`
	Targets   []string     `json:"resolved_targets"`
	Feedback  string       `json:"feedback"`
}

type CoderAttempt = TaskAttempt
type TaskRunFunc func(context.Context, TaskAttempt) (TaskResult, error)
type CoderRunFunc = TaskRunFunc
type PlannerReviewFunc func(context.Context, TaskReviewRequest) (TaskReview, error)

type ExecutionLoop struct {
	Resolver    TargetResolver
	Coder       CoderRunFunc
	Executors   map[string]TaskRunFunc
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
	TaskIndex int        `json:"task_index"`
	Title     string     `json:"title"`
	Executor  string     `json:"executor"`
	Attempts  int        `json:"attempts"`
	Result    TaskResult `json:"result"`
}

// ExecutionCheckpoint is the durable state machine for an approved plan.
// ExecutorRunning is deliberately distinct from ExecutorPending: after a process
// interruption it must be converted into a new recovery attempt instead of
// blindly replaying a possibly side-effecting executor invocation.
type ExecutionCheckpoint struct {
	ExecutionID    string                `json:"execution_id,omitempty"`
	RunID          string                `json:"run_id,omitempty"`
	Objective      string                `json:"objective,omitempty"`
	Planning       *PlanningLog          `json:"planning,omitempty"`
	ExecutionLog   *ExecutionLog         `json:"execution_log,omitempty"`
	Phase          ExecutionPhase        `json:"phase"`
	Plan           PlanProposal          `json:"plan"`
	TaskIndex      int                   `json:"task_index"`
	Attempt        int                   `json:"attempt"`
	ActiveExecutor string                `json:"active_executor,omitempty"`
	Targets        []string              `json:"resolved_targets,omitempty"`
	Feedback       string                `json:"feedback"`
	PendingResult  *TaskResult           `json:"pending_result,omitempty"`
	CompletedTasks int                   `json:"completed_tasks"`
	Attempts       int                   `json:"attempts"`
	TaskAttempts   int                   `json:"task_attempts"`
	ResumeCount    int                   `json:"resume_count"`
	Tasks          []TaskExecutionResult `json:"tasks,omitempty"`
}

func NewExecutionCheckpoint(plan PlanProposal) ExecutionCheckpoint {
	checkpoint := ExecutionCheckpoint{Phase: ExecutionPhaseTarget, Plan: plan, Attempt: 1}
	for index := range checkpoint.Plan.Steps {
		checkpoint.Plan.Steps[index].Executor = strings.TrimSpace(checkpoint.Plan.Steps[index].Executor)
		if checkpoint.Plan.Steps[index].Executor == "" {
			checkpoint.Plan.Steps[index].Executor = PlanExecutorCoder
		}
	}
	checkpoint.ActiveExecutor = strings.TrimSpace(checkpoint.ActiveExecutor)
	return checkpoint
}

// MigrateExecutionCheckpointV1 upgrades the persisted Coder-only state shape
// to the executor-neutral contract. The caller remains responsible for writing
// the migrated checkpoint at the next durability boundary.
func MigrateExecutionCheckpointV1(checkpoint ExecutionCheckpoint) (ExecutionCheckpoint, error) {
	return NormalizeExecutionCheckpoint(checkpoint)
}

// NormalizeExecutionCheckpoint fills compatibility defaults accepted from
// legacy callers before the state is validated or persisted.
func NormalizeExecutionCheckpoint(checkpoint ExecutionCheckpoint) (ExecutionCheckpoint, error) {
	for index := range checkpoint.Plan.Steps {
		checkpoint.Plan.Steps[index].Executor = strings.TrimSpace(checkpoint.Plan.Steps[index].Executor)
		if checkpoint.Plan.Steps[index].Executor == "" {
			checkpoint.Plan.Steps[index].Executor = PlanExecutorCoder
		}
	}
	switch checkpoint.Phase {
	case ExecutionPhase("coder_pending"):
		checkpoint.Phase = ExecutionPhaseExecutorPending
	case ExecutionPhase("coder_running"):
		checkpoint.Phase = ExecutionPhaseExecutorRunning
	}
	checkpoint.ActiveExecutor = strings.TrimSpace(checkpoint.ActiveExecutor)
	if checkpoint.ActiveExecutor == "" {
		switch checkpoint.Phase {
		case ExecutionPhaseExecutorPending, ExecutionPhaseExecutorRunning, ExecutionPhaseReviewPending:
			checkpoint.ActiveExecutor = PlanExecutorCoder
		}
	}
	if checkpoint.PendingResult != nil {
		checkpoint.PendingResult.Executor = strings.TrimSpace(checkpoint.PendingResult.Executor)
		if checkpoint.PendingResult.Executor == "" {
			checkpoint.PendingResult.Executor = checkpoint.ActiveExecutor
		}
	}
	for index := range checkpoint.Tasks {
		checkpoint.Tasks[index].Executor = strings.TrimSpace(checkpoint.Tasks[index].Executor)
		if checkpoint.Tasks[index].Executor == "" {
			checkpoint.Tasks[index].Executor = PlanExecutorCoder
		}
		checkpoint.Tasks[index].Result.Executor = strings.TrimSpace(checkpoint.Tasks[index].Result.Executor)
		if checkpoint.Tasks[index].Result.Executor == "" {
			checkpoint.Tasks[index].Result.Executor = checkpoint.Tasks[index].Executor
		}
	}
	if err := ValidateExecutionCheckpoint(checkpoint); err != nil {
		return ExecutionCheckpoint{}, err
	}
	return checkpoint, nil
}

// PrepareExecutionResume makes an interrupted executor invocation safe to
// continue. Other phases are already idempotent: target resolution is read-only
// and Planner review has no workspace mutation tools.
func PrepareExecutionResume(checkpoint ExecutionCheckpoint) (ExecutionCheckpoint, error) {
	if err := ValidateExecutionCheckpoint(checkpoint); err != nil {
		return ExecutionCheckpoint{}, err
	}
	checkpoint.ResumeCount++
	if checkpoint.Phase == ExecutionPhaseExecutorRunning {
		checkpoint.Phase = ExecutionPhaseExecutorPending
		checkpoint.Attempt++
		checkpoint.PendingResult = nil
		interruptedFeedback := interruptedExecutorFeedback
		if checkpoint.ActiveExecutor == PlanExecutorCoder {
			interruptedFeedback = interruptedCoderFeedback
		}
		if strings.TrimSpace(checkpoint.Feedback) == "" {
			checkpoint.Feedback = interruptedFeedback
		} else {
			checkpoint.Feedback = strings.TrimSpace(checkpoint.Feedback) + "\n\n" + interruptedFeedback
		}
	}
	return checkpoint, nil
}

func ValidateExecutionCheckpoint(checkpoint ExecutionCheckpoint) error {
	if len(checkpoint.Plan.Steps) == 0 {
		return errors.New("subagent: execution checkpoint has no plan tasks")
	}
	for index := range checkpoint.Plan.Steps {
		if err := validatePlanExecutor(checkpoint.Plan.Steps[index].EffectiveExecutor()); err != nil {
			return fmt.Errorf("subagent: execution checkpoint task %d: %w", index+1, err)
		}
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
	case ExecutionPhaseExecutorPending, ExecutionPhaseExecutorRunning:
		if checkpoint.TaskIndex >= len(checkpoint.Plan.Steps) || len(cleanStrings(checkpoint.Targets)) == 0 {
			return errors.New("subagent: executor checkpoint requires a current task and resolved targets")
		}
		if err := validatePlanExecutor(checkpoint.ActiveExecutor); err != nil {
			return fmt.Errorf("subagent: executor checkpoint: %w", err)
		}
		if err := validateResolvedTargets(checkpoint.Targets); err != nil {
			return err
		}
	case ExecutionPhaseReviewPending:
		if checkpoint.TaskIndex >= len(checkpoint.Plan.Steps) || len(cleanStrings(checkpoint.Targets)) == 0 || checkpoint.PendingResult == nil {
			return errors.New("subagent: review checkpoint requires targets and an executor result")
		}
		if err := validatePlanExecutor(checkpoint.ActiveExecutor); err != nil {
			return fmt.Errorf("subagent: review checkpoint: %w", err)
		}
		if err := validateResolvedTargets(checkpoint.Targets); err != nil {
			return err
		}
		result := *checkpoint.PendingResult
		result.Executor = strings.TrimSpace(result.Executor)
		if result.Executor == "" {
			result.Executor = checkpoint.ActiveExecutor
		}
		if result.Executor != checkpoint.ActiveExecutor {
			return errors.New("subagent: review checkpoint result executor does not match the active executor")
		}
		if err := validateTaskResult(result); err != nil {
			return fmt.Errorf("subagent: execution checkpoint executor result: %w", err)
		}
		if checkpoint.TaskAttempts < 1 {
			return errors.New("subagent: review checkpoint requires a completed executor attempt")
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

func validatePlanExecutor(executor string) error {
	switch strings.TrimSpace(executor) {
	case PlanExecutorCoder, PlanExecutorExternalWebTester:
		return nil
	default:
		return fmt.Errorf("unsupported plan executor %q", executor)
	}
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
	if (l.Coder == nil && len(l.Executors) == 0) || l.Review == nil {
		return PlanExecutionResult{}, errors.New("subagent: execution requires executor and Planner review functions")
	}
	if err := ValidateExecutionCheckpoint(checkpoint); err != nil {
		return PlanExecutionResult{}, err
	}
	maximumAttempts := l.MaxAttempts
	if maximumAttempts <= 0 {
		maximumAttempts = defaultTaskAttempts
	}
	if checkpoint.Phase == ExecutionPhaseExecutorRunning {
		return checkpoint.result(), errors.New("subagent: interrupted executor checkpoint requires PrepareExecutionResume")
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
			checkpoint.ActiveExecutor = checkpoint.Plan.Steps[taskIndex].EffectiveExecutor()
			checkpoint.Attempt = 1
			checkpoint.TaskAttempts = 0
			checkpoint.Phase = ExecutionPhaseExecutorPending
			if err := l.persist(ctx, checkpoint); err != nil {
				return checkpoint.result(), err
			}
			reportProgress(l.Progress, ProgressEvent{
				Agent: "executor", Action: ProgressCompleted,
				Detail: fmt.Sprintf("task %d target · %d files", taskIndex+1, len(targets)),
			})
		case ExecutionPhaseExecutorPending:
			if checkpoint.TaskAttempts >= maximumAttempts {
				return checkpoint.result(), fmt.Errorf("subagent: task %d exhausted %d attempts", taskIndex+1, maximumAttempts)
			}
			run, found := l.executor(checkpoint.ActiveExecutor)
			if !found {
				return checkpoint.result(), fmt.Errorf("subagent: task %d requires unavailable executor %q; restore its enabled agent connection and resume the saved checkpoint", taskIndex+1, checkpoint.ActiveExecutor)
			}
			reportProgress(l.Progress, ProgressEvent{
				Agent: checkpoint.ActiveExecutor, Action: ProgressStarted,
				Detail: fmt.Sprintf("task %d/%d · attempt %d", taskIndex+1, len(checkpoint.Plan.Steps), checkpoint.Attempt),
			})
			checkpoint.Phase = ExecutionPhaseExecutorRunning
			if err := l.persist(ctx, checkpoint); err != nil {
				return checkpoint.result(), err
			}
			executorResult, err := run(ctx, TaskAttempt{
				Plan: checkpoint.Plan, TaskIndex: taskIndex, Attempt: checkpoint.Attempt,
				Executor: checkpoint.ActiveExecutor, Targets: append([]string(nil), checkpoint.Targets...), Feedback: checkpoint.Feedback,
			})
			if err != nil {
				reportProgress(l.Progress, ProgressEvent{Agent: checkpoint.ActiveExecutor, Action: ProgressFailed, Detail: err.Error()})
				return checkpoint.result(), fmt.Errorf("subagent: executor %q task %d attempt %d: %w", checkpoint.ActiveExecutor, taskIndex+1, checkpoint.Attempt, err)
			}
			executorResult.Executor = checkpoint.ActiveExecutor
			if err := validateTaskResult(executorResult); err != nil {
				return checkpoint.result(), fmt.Errorf("subagent: executor %q task %d attempt %d result: %w", checkpoint.ActiveExecutor, taskIndex+1, checkpoint.Attempt, err)
			}
			checkpoint.Attempts++
			checkpoint.TaskAttempts++
			checkpoint.PendingResult = &executorResult
			checkpoint.Phase = ExecutionPhaseReviewPending
			if err := l.persist(ctx, checkpoint); err != nil {
				return checkpoint.result(), err
			}
			reportProgress(l.Progress, ProgressEvent{Agent: checkpoint.ActiveExecutor, Action: ProgressCompleted, Detail: executorResult.Summary})
		case ExecutionPhaseReviewPending:
			if _, found := l.executor(checkpoint.ActiveExecutor); !found {
				return checkpoint.result(), fmt.Errorf("subagent: task %d requires unavailable executor %q to review or resume its saved result; restore its enabled agent connection", taskIndex+1, checkpoint.ActiveExecutor)
			}
			review, err := l.Review(ctx, TaskReviewRequest{
				Plan: checkpoint.Plan, TaskIndex: taskIndex, Attempt: checkpoint.Attempt,
				Targets: append([]string(nil), checkpoint.Targets...), Executor: checkpoint.ActiveExecutor,
				Result: *checkpoint.PendingResult,
			})
			if err != nil {
				return checkpoint.result(), fmt.Errorf("subagent: review task %d attempt %d: %w", taskIndex+1, checkpoint.Attempt, err)
			}
			updatedPlan := checkpoint.Plan
			if err := ApplyTaskReview(&updatedPlan, review); err != nil {
				return checkpoint.result(), err
			}
			if review.Decision == "next" {
				acceptanceExecutor := checkpoint.Plan.Steps[taskIndex].EffectiveExecutor()
				if checkpoint.ActiveExecutor != acceptanceExecutor {
					return checkpoint.result(), fmt.Errorf("subagent: task %d can advance only after acceptance executor %q runs; current executor is %q", taskIndex+1, acceptanceExecutor, checkpoint.ActiveExecutor)
				}
				if checkpoint.ActiveExecutor == PlanExecutorExternalWebTester && checkpoint.PendingResult.Outcome != "succeeded" {
					return checkpoint.result(), fmt.Errorf("subagent: task %d external_web_tester result must succeed before next", taskIndex+1)
				}
				checkpoint.Plan = updatedPlan
				checkpoint.CompletedTasks++
				checkpoint.Tasks = append(checkpoint.Tasks, TaskExecutionResult{
					TaskIndex: taskIndex, Title: checkpoint.Plan.Steps[taskIndex].Title,
					Executor: checkpoint.ActiveExecutor, Attempts: checkpoint.TaskAttempts, Result: *checkpoint.PendingResult,
				})
				checkpoint.TaskIndex++
				checkpoint.Targets = nil
				checkpoint.Feedback = ""
				checkpoint.PendingResult = nil
				checkpoint.ActiveExecutor = ""
				checkpoint.Attempt = 1
				checkpoint.TaskAttempts = 0
				if checkpoint.TaskIndex == len(checkpoint.Plan.Steps) {
					checkpoint.Phase = ExecutionPhaseCompleted
				} else {
					checkpoint.Phase = ExecutionPhaseTarget
				}
			} else {
				nextExecutor := strings.TrimSpace(review.NextExecutor)
				if nextExecutor == "" {
					nextExecutor = checkpoint.ActiveExecutor
				}
				if _, found := l.executor(nextExecutor); !found {
					return checkpoint.result(), fmt.Errorf("subagent: task %d retry requires unavailable executor %q; restore its enabled agent connection and resume the saved checkpoint", taskIndex+1, nextExecutor)
				}
				checkpoint.Plan = updatedPlan
				checkpoint.Feedback = review.Feedback
				checkpoint.PendingResult = nil
				checkpoint.ActiveExecutor = nextExecutor
				checkpoint.Attempt++
				checkpoint.Phase = ExecutionPhaseExecutorPending
			}
			if err := l.persist(ctx, checkpoint); err != nil {
				return checkpoint.result(), err
			}
		}
	}
	return checkpoint.result(), nil
}

func (l ExecutionLoop) executor(name string) (TaskRunFunc, bool) {
	name = strings.TrimSpace(name)
	if run := l.Executors[name]; run != nil {
		return run, true
	}
	if name == PlanExecutorCoder && l.Coder != nil {
		return l.Coder, true
	}
	return nil, false
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
	body.WriteString(fmt.Sprintf("\n\nCompleted %d task(s) in %d executor attempt(s).", result.CompletedTasks, result.Attempts))
	for _, task := range result.Tasks {
		body.WriteString(fmt.Sprintf("\n\n%d. %s", task.TaskIndex+1, task.Title))
		body.WriteString(fmt.Sprintf("\n   %s", task.Result.Summary))
		body.WriteString(fmt.Sprintf("\n   executor: %s", task.Executor))
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
