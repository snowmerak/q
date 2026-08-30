package subagent

import "time"

const (
	PlanningOutcomeApproved = "approved"
	PlanningOutcomeCanceled = "canceled"
	PlanningOutcomeFailed   = "failed"

	PlanningEventInput      = "input"
	PlanningEventActivity   = "activity"
	PlanningEventAssistant  = TraceAssistant
	PlanningEventToolCall   = TraceToolCall
	PlanningEventToolResult = TraceToolResult
	PlanningEventQuestion   = "question"
	PlanningEventAnswer     = "answer"
)

// PlanningLog is the human-readable audit trail for the approval-gated part of
// /plan. It contains only user-visible activity and model-returned trace data;
// providers' hidden chain-of-thought is neither available nor reconstructed.
type PlanningLog struct {
	RunID         string          `json:"run_id,omitempty"`
	Objective     string          `json:"objective"`
	StartedAt     time.Time       `json:"started_at"`
	CompletedAt   time.Time       `json:"completed_at"`
	Outcome       string          `json:"outcome"`
	Error         string          `json:"error,omitempty"`
	Cycles        int             `json:"cycles"`
	Brief         *GrillBrief     `json:"brief,omitempty"`
	Plan          *PlanProposal   `json:"plan,omitempty"`
	Events        []PlanningEvent `json:"events,omitempty"`
	Truncated     bool            `json:"truncated,omitempty"`
	DroppedEvents int             `json:"dropped_events,omitempty"`
}

// PlanningEvent keeps planning activity, visible assistant messages, tool
// traffic, and user interaction in one chronological sequence.
type PlanningEvent struct {
	Sequence  int           `json:"sequence"`
	At        time.Time     `json:"at"`
	Type      string        `json:"type"`
	Agent     string        `json:"agent,omitempty"`
	TaskID    string        `json:"task_id,omitempty"`
	ParentID  string        `json:"parent_id,omitempty"`
	Action    string        `json:"action,omitempty"`
	Detail    string        `json:"detail,omitempty"`
	CallID    string        `json:"call_id,omitempty"`
	Name      string        `json:"name,omitempty"`
	Content   string        `json:"content,omitempty"`
	Truncated bool          `json:"truncated,omitempty"`
	IsError   bool          `json:"is_error,omitempty"`
	Question  *UserQuestion `json:"question,omitempty"`
	Answer    *UserAnswer   `json:"answer,omitempty"`
}
