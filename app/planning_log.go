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
	maximumPlanningLogEvents       = 2048
	maximumPlanningLogContentBytes = 3 << 20
	maximumPlanningEventContent    = 256 << 10
)

type planningLogRecorder struct {
	mu           sync.Mutex
	log          subagent.PlanningLog
	contentBytes int
	finished     bool
}

func newPlanningLogRecorder(runID, objective string, contextValues []string) *planningLogRecorder {
	recorder := &planningLogRecorder{log: subagent.PlanningLog{
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

func (r *planningLogRecorder) recordProgress(progress subagent.ProgressEvent) {
	r.append(subagent.PlanningEvent{
		Type:  subagent.PlanningEventActivity,
		Agent: progress.Agent, TaskID: progress.TaskID, ParentID: progress.ParentID,
		Action: progress.Action, Detail: progress.Detail,
	})
}

func (r *planningLogRecorder) recordTrace(trace subagent.TraceEvent) {
	r.append(subagent.PlanningEvent{
		Type: trace.Kind, Agent: trace.Agent, TaskID: trace.TaskID, ParentID: trace.ParentID,
		CallID: trace.CallID, Name: trace.Name, Content: trace.Content, IsError: trace.IsError,
	})
}

func (r *planningLogRecorder) recordQuestion(question subagent.UserQuestion) {
	copy := question
	copy.Choices = append([]subagent.UserChoice(nil), question.Choices...)
	r.append(subagent.PlanningEvent{Type: subagent.PlanningEventQuestion, Agent: "user", Question: &copy})
}

func (r *planningLogRecorder) recordAnswer(answer subagent.UserAnswer, err error) {
	copy := answer
	event := subagent.PlanningEvent{Type: subagent.PlanningEventAnswer, Agent: "user", Answer: &copy}
	if err != nil {
		event.IsError = true
		event.Content = err.Error()
	}
	r.append(event)
}

func (r *planningLogRecorder) append(event subagent.PlanningEvent) {
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

func (r *planningLogRecorder) boundString(value string) (string, bool) {
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

func (r *planningLogRecorder) finish(result subagent.PlanWorkflowResult, outcome string, runErr error) subagent.PlanningLog {
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
	value.Events = append([]subagent.PlanningEvent(nil), value.Events...)
	return value
}
