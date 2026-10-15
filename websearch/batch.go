package websearch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/snowmerak/q/diagnostics"
)

// Searcher owns independent resources per call and must support concurrent
// searches and context cancellation. Batch scheduling does not own the service.
type Searcher interface {
	Search(context.Context, Input) (Output, error)
}

type BatchTask struct {
	ID     string `json:"id" jsonschema:"Unique task identifier, 1 to 64 ASCII letters, digits, dots, underscores or hyphens."`
	Search Input  `json:"search" jsonschema:"An independent web_search request. Preserve this task's entire goal and source constraints."`
}

type BatchInput struct {
	Tasks            []BatchTask `json:"tasks" jsonschema:"One to eight independent research tasks. Each has its own browser, evidence, requirements and completion check."`
	Concurrency      int         `json:"concurrency,omitempty" jsonschema:"Concurrent search loops, default 3, maximum 4. The 35 minute batch deadline includes queue time and respects the caller deadline."`
	ModelConcurrency int         `json:"model_concurrency,omitempty" jsonschema:"Concurrent model requests in the built-in research engine, default 1, maximum 4. Independent from search-loop concurrency; includes native decisions and generative planning. Increase only when the model endpoint can handle overlapping inference."`
}

type TaskSummary struct {
	ID              string  `json:"id"`
	Status          string  `json:"status"`
	Started         bool    `json:"started"`
	ElapsedSeconds  float64 `json:"elapsed_seconds"`
	EvidenceCount   int     `json:"evidence_count"`
	UnresolvedCount int     `json:"unresolved_count"`
	ErrorCount      int     `json:"error_count"`
}

// BatchOverview keeps all task outcomes visible before evidence-heavy results
// in a bounded, alphabetically serialized Loom preview.
type BatchOverview struct {
	Status string        `json:"status"`
	Tasks  []TaskSummary `json:"tasks"`
}

type TaskError struct {
	Code    string `json:"code"` // search_error, invalid_result, cancelled, deadline_exceeded
	Message string `json:"message"`
}

type TaskResult struct {
	ID             string     `json:"id"`
	Status         string     `json:"status"` // complete, incomplete, error, cancelled
	Started        bool       `json:"started"`
	ElapsedSeconds float64    `json:"elapsed_seconds"` // excludes admission queue time
	Result         *Output    `json:"result,omitempty"`
	Error          *TaskError `json:"error,omitempty"`
}

type BatchOutput struct {
	Status           string        `json:"status"` // complete only when every task is complete
	Concurrency      int           `json:"concurrency"`
	ModelConcurrency int           `json:"model_concurrency"`
	Overview         BatchOverview `json:"overview"`
	ElapsedSeconds   float64       `json:"elapsed_seconds"` // includes admission queue time
	SystemOneCalls   int           `json:"system_one_calls"`
	CoverageCalls    int           `json:"coverage_calls"`
	PlannerCalls     int           `json:"planner_calls"`
	Results          []TaskResult  `json:"results"` // same order as input tasks
}

// SearchBatch schedules existing search calls; it adds no model orchestration.
// Invalid batches start no searches. Failures do not cancel sibling tasks.
// Cancellation stops admission and joins started calls before returning their
// partial results. Concurrency is bounded per batch, not across other callers.
func SearchBatch(ctx context.Context, search Searcher, in BatchInput) (output BatchOutput, returnErr error) {
	if search == nil {
		return BatchOutput{}, errors.New("web_search_batch: search service is required")
	}
	if len(in.Tasks) < 1 || len(in.Tasks) > 8 || in.Concurrency < 0 || in.Concurrency > 4 || in.ModelConcurrency < 0 || in.ModelConcurrency > 4 {
		return BatchOutput{}, errors.New("web_search_batch: provide 1 to 8 tasks; concurrency and model_concurrency must be between 0 and 4")
	}
	seen := map[string]bool{}
	for _, task := range in.Tasks {
		if !validTaskID(task.ID) || seen[task.ID] {
			return BatchOutput{}, errors.New("web_search_batch: task IDs must be unique, 1 to 64 ASCII letters, digits, dots, underscores or hyphens")
		}
		seen[task.ID] = true
		if err := validate(task.Search); err != nil {
			return BatchOutput{}, fmt.Errorf("web_search_batch: task %q: %w", task.ID, err)
		}
	}
	concurrency := in.Concurrency
	if concurrency == 0 {
		concurrency = 3
	}
	concurrency = min(concurrency, len(in.Tasks))
	modelConcurrency := in.ModelConcurrency
	if modelConcurrency == 0 {
		modelConcurrency = 1
	}
	modelConcurrency = min(modelConcurrency, concurrency)
	started := time.Now()
	ctx = diagnostics.WithAttrs(ctx, slog.String("search_batch_id", uuid.NewString()))
	diagnostics.Logger(ctx).Info("web_search.batch.started", "task_count", len(in.Tasks), "concurrency", concurrency, "model_concurrency", modelConcurrency)
	defer func() {
		diagnostics.Logger(ctx).Info("web_search.batch.completed", "status", output.Status, "task_count", len(output.Results), "duration_ms", time.Since(started).Milliseconds(), "reason_code", failureCode(returnErr))
	}()
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	ctx = context.WithValue(ctx, modelSlotsKey{}, make(chan struct{}, modelConcurrency))
	out := BatchOutput{Status: "complete", Concurrency: concurrency, ModelConcurrency: modelConcurrency, Results: make([]TaskResult, len(in.Tasks))}
	for i, task := range in.Tasks {
		out.Results[i] = TaskResult{ID: task.ID, Status: "cancelled"}
	}
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range concurrency {
		workers.Go(func() {
			for index := range jobs {
				if ctx.Err() != nil {
					continue
				}
				task := in.Tasks[index]
				begin := time.Now()
				taskContext := diagnostics.WithAttrs(ctx, slog.String("search_task_id", task.ID))
				diagnostics.Logger(taskContext).Info("web_search.task.started")
				result, err := search.Search(taskContext, task.Search)
				diagnostics.Logger(taskContext).Info("web_search.task.completed", "status", result.Status, "stop_reason", result.StopReason, "duration_ms", time.Since(begin).Milliseconds(), "reason_code", failureCode(err))
				item := TaskResult{ID: task.ID, Started: true, ElapsedSeconds: time.Since(begin).Seconds(), Status: result.Status}
				if result.Status != "" || len(result.Evidence) > 0 || result.Steps > 0 {
					item.Result = &result
				}
				if err != nil {
					item.Status = "error"
					item.Error = &TaskError{Code: "search_error", Message: clip(err.Error(), 600)}
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						item.Status = "cancelled"
						item.Error = cancellationError(err)
					}
				} else if result.Status != "complete" && ctx.Err() != nil {
					item.Status = "cancelled"
					item.Error = cancellationError(ctx.Err())
				} else if result.Status != "complete" && result.Status != "incomplete" {
					item.Status = "error"
					item.Error = &TaskError{Code: "invalid_result", Message: "Search service returned an invalid status"}
				}
				// Each worker writes a different slot; readers wait for all workers.
				out.Results[index] = item
			}
		})
	}
admit:
	for index := range in.Tasks {
		select {
		case jobs <- index:
		case <-ctx.Done():
			break admit
		}
	}
	close(jobs)
	workers.Wait()
	for i := range out.Results {
		item := &out.Results[i]
		if !item.Started {
			item.Error = cancellationError(ctx.Err())
		}
		if item.Status != "complete" {
			out.Status = "incomplete"
		}
		if item.Result != nil {
			out.SystemOneCalls += item.Result.SystemOneCalls
			out.CoverageCalls += item.Result.CoverageCalls
			out.PlannerCalls += item.Result.PlannerCalls
		}
		summary := TaskSummary{ID: item.ID, Status: item.Status, Started: item.Started, ElapsedSeconds: item.ElapsedSeconds}
		if item.Error != nil {
			summary.ErrorCount++
		}
		if item.Result != nil {
			summary.EvidenceCount = len(item.Result.Evidence)
			summary.UnresolvedCount = len(item.Result.Unresolved)
			summary.ErrorCount += len(item.Result.Errors)
		}
		out.Overview.Tasks = append(out.Overview.Tasks, summary)
	}
	out.Overview.Status = out.Status
	out.ElapsedSeconds = time.Since(started).Seconds()
	return out, nil
}

func validTaskID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, ch := range id {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '.' || ch == '_' || ch == '-' {
			continue
		}
		return false
	}
	return true
}

type modelSlotsKey struct{}

// AcquireModelSlot lets an injected search service honor the batch's model
// concurrency. Engine uses it for native decisions and generative planning.
// Outside a batch it is a no-op. Release the returned slot after one model call.
func AcquireModelSlot(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slots, _ := ctx.Value(modelSlotsKey{}).(chan struct{})
	if slots == nil {
		return func() {}, nil
	}
	select {
	case slots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-slots }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func cancellationError(err error) *TaskError {
	if errors.Is(err, context.DeadlineExceeded) {
		return &TaskError{Code: "deadline_exceeded", Message: "Search deadline exceeded"}
	}
	return &TaskError{Code: "cancelled", Message: "Search cancelled"}
}
