package app

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/snowmerak/q/subagent"
)

const (
	maximumPlanningLogEvents       = 1024
	maximumPlanningLogContentBytes = 1 << 20
	maximumPlanningEventContent    = 128 << 10
)

type auditLogRecorder struct {
	mu           sync.Mutex
	log          subagent.PlanningLog
	contentBytes int
	finished     bool
	completedAt  *time.Time
}

func newPlanningLogRecorder(runID, objective string, contextValues []string) *auditLogRecorder {
	recorder := &auditLogRecorder{log: subagent.PlanningLog{
		RunID: strings.TrimSpace(runID), Objective: strings.TrimSpace(objective),
		StartedAt: time.Now().UTC(),
	}}
	input, err := json.MarshalIndent(subagent.GrillTask{
		Objective: objective, Context: append([]string(nil), contextValues...),
	}, "", "  ")
	if err == nil {
		recorder.append(subagent.PlanningEvent{
			Type: subagent.PlanningEventInput, Agent: "plan", Content: string(input),
		})
	}
	return recorder
}

func newDebugLogRecorder(runID, objective string, contextValues []string) *auditLogRecorder {
	recorder := newPlanningLogRecorder(runID, objective, contextValues)
	if len(recorder.log.Events) > 0 && recorder.log.Events[0].Type == subagent.PlanningEventInput {
		recorder.log.Events[0].Agent = "debug"
	}
	return recorder
}

func newExecutionLogRecorder(existing *subagent.ExecutionLog) *auditLogRecorder {
	startedAt := time.Now().UTC()
	recorder := &auditLogRecorder{log: subagent.PlanningLog{StartedAt: startedAt}}
	if existing == nil {
		return recorder
	}
	recorder.log.StartedAt = existing.StartedAt
	if recorder.log.StartedAt.IsZero() {
		recorder.log.StartedAt = startedAt
	}
	recorder.log.Events = clonePlanningEvents(existing.Events)
	recorder.log.Truncated = existing.Truncated
	recorder.log.DroppedEvents = existing.DroppedEvents
	if existing.CompletedAt != nil {
		completedAt := existing.CompletedAt.UTC()
		recorder.completedAt = &completedAt
		recorder.finished = true
	}
	for _, event := range recorder.log.Events {
		recorder.contentBytes += planningEventContentBytes(event)
	}
	return recorder
}

func (r *auditLogRecorder) recordProgress(progress subagent.ProgressEvent) {
	r.append(subagent.PlanningEvent{
		Type:  subagent.PlanningEventActivity,
		Agent: progress.Agent, TaskID: progress.TaskID, ParentID: progress.ParentID,
		Action: progress.Action, Detail: progress.Detail,
	})
}

func (r *auditLogRecorder) recordTrace(trace subagent.TraceEvent) {
	r.append(subagent.PlanningEvent{
		Type: trace.Kind, Agent: trace.Agent, TaskID: trace.TaskID, ParentID: trace.ParentID,
		CallID: trace.CallID, Name: trace.Name, Content: trace.Content, IsError: trace.IsError,
	})
}

func (r *auditLogRecorder) recordQuestion(question subagent.UserQuestion) {
	copy := question
	copy.Choices = append([]subagent.UserChoice(nil), question.Choices...)
	r.append(subagent.PlanningEvent{Type: subagent.PlanningEventQuestion, Agent: "user", Question: &copy})
}

func (r *auditLogRecorder) recordAnswer(answer subagent.UserAnswer, err error) {
	copy := answer
	source := strings.TrimSpace(answer.Source)
	if source == "" {
		source = subagent.UserAnswerSourceUser
		copy.Source = source
	}
	event := subagent.PlanningEvent{Type: subagent.PlanningEventAnswer, Agent: source, Answer: &copy}
	if err != nil {
		event.IsError = true
		event.Content = err.Error()
	}
	r.append(event)
}

func (r *auditLogRecorder) append(event subagent.PlanningEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return
	}
	if len(r.log.Events) >= maximumPlanningLogEvents {
		r.log.Truncated = true
		r.log.DroppedEvents++
		return
	}
	event.Sequence = len(r.log.Events) + 1
	event.At = time.Now().UTC()
	var truncated bool
	event.Detail, truncated = r.boundString(event.Detail)
	event.Truncated = event.Truncated || truncated
	event.Content, truncated = r.boundString(event.Content)
	event.Truncated = event.Truncated || truncated
	if event.Question != nil {
		event.Question.Question, truncated = r.boundString(event.Question.Question)
		event.Truncated = event.Truncated || truncated
		event.Question.Context, truncated = r.boundString(event.Question.Context)
		event.Truncated = event.Truncated || truncated
		for index := range event.Question.Choices {
			choice := &event.Question.Choices[index]
			choice.ID, truncated = r.boundString(choice.ID)
			event.Truncated = event.Truncated || truncated
			choice.Label, truncated = r.boundString(choice.Label)
			event.Truncated = event.Truncated || truncated
			choice.Description, truncated = r.boundString(choice.Description)
			event.Truncated = event.Truncated || truncated
		}
	}
	if event.Answer != nil {
		event.Answer.SelectedChoiceID, truncated = r.boundString(event.Answer.SelectedChoiceID)
		event.Truncated = event.Truncated || truncated
		event.Answer.Freeform, truncated = r.boundString(event.Answer.Freeform)
		event.Truncated = event.Truncated || truncated
	}
	if event.Truncated {
		r.log.Truncated = true
	}
	r.log.Events = append(r.log.Events, event)
}

func (r *auditLogRecorder) boundString(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	available := maximumPlanningLogContentBytes - r.contentBytes
	limit := min(maximumPlanningEventContent, available)
	if limit <= 0 {
		return "", true
	}
	truncated := len(value) > limit
	if truncated {
		value = truncateUTF8(value, limit)
	}
	r.contentBytes += len(value)
	return value, truncated
}

func (r *auditLogRecorder) finish(result subagent.PlanWorkflowResult, outcome string, runErr error) subagent.PlanningLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.finished {
		r.finished = true
		r.log.CompletedAt = time.Now().UTC()
		r.log.Outcome = strings.TrimSpace(outcome)
		r.log.Cycles = result.Cycles
		if strings.TrimSpace(result.Brief.Objective) != "" {
			brief := result.Brief
			r.log.Brief = &brief
		}
		if strings.TrimSpace(result.Plan.Outcome) != "" || strings.TrimSpace(result.Plan.Summary) != "" ||
			strings.TrimSpace(result.Plan.Blocker) != "" || len(result.Plan.Steps) > 0 {
			plan := result.Plan
			r.log.Plan = &plan
		}
		if runErr != nil {
			r.log.Error = runErr.Error()
		}
	}
	return clonePlanningLog(r.log)
}

func (r *auditLogRecorder) finishDebug(
	result subagent.DebugWorkflowResult,
	outcome string,
	runErr error,
) subagent.DebugExecutionLog {
	planning := r.finish(subagent.PlanWorkflowResult{Brief: result.Brief}, outcome, runErr)
	debug := subagent.DebugExecutionLog{
		RunID: planning.RunID, Objective: planning.Objective,
		StartedAt: planning.StartedAt, CompletedAt: planning.CompletedAt,
		Outcome: planning.Outcome, Error: planning.Error,
		Brief: planning.Brief, Events: planning.Events,
		Truncated: planning.Truncated, DroppedEvents: planning.DroppedEvents,
	}
	if strings.TrimSpace(result.Report.Summary) != "" || strings.TrimSpace(result.Report.LikelyCause) != "" ||
		len(result.Report.Findings) > 0 || len(result.Report.SuggestedFixes) > 0 {
		report := result.Report
		debug.Report = &report
	}
	return debug
}

func (r *auditLogRecorder) executionSnapshot(completed bool) *subagent.ExecutionLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	if completed && r.completedAt == nil {
		completedAt := time.Now().UTC()
		r.completedAt = &completedAt
		r.finished = true
	}
	var completedAt *time.Time
	if r.completedAt != nil {
		copy := *r.completedAt
		completedAt = &copy
	}
	return &subagent.ExecutionLog{
		StartedAt: r.log.StartedAt, CompletedAt: completedAt,
		Events: clonePlanningEvents(r.log.Events), Truncated: r.log.Truncated,
		DroppedEvents: r.log.DroppedEvents,
	}
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value
}

func clonePlanningLog(value subagent.PlanningLog) subagent.PlanningLog {
	value.Events = clonePlanningEvents(value.Events)
	return value
}

func clonePlanningEvents(events []subagent.PlanningEvent) []subagent.PlanningEvent {
	return append([]subagent.PlanningEvent(nil), events...)
}

func planningEventContentBytes(event subagent.PlanningEvent) int {
	total := len(event.Detail) + len(event.Content)
	if event.Question != nil {
		total += len(event.Question.Question) + len(event.Question.Context)
		for _, choice := range event.Question.Choices {
			total += len(choice.ID) + len(choice.Label) + len(choice.Description)
		}
	}
	if event.Answer != nil {
		total += len(event.Answer.SelectedChoiceID) + len(event.Answer.Freeform)
	}
	return total
}
