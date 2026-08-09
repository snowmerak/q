package subagent

import "strings"

const (
	ProgressStarted   = "started"
	ProgressThinking  = "thinking"
	ProgressTool      = "tool"
	ProgressDelegated = "delegated"
	ProgressWaiting   = "waiting"
	ProgressResumed   = "resumed"
	ProgressCompleted = "completed"
	ProgressFailed    = "failed"
)

// ProgressEvent is a bounded, user-facing projection of subagent activity.
// It intentionally carries no prompts or tool results; those remain in the
// lifecycle archive while the TUI receives enough detail to explain progress.
type ProgressEvent struct {
	Agent    string
	TaskID   string
	ParentID string
	Action   string
	Detail   string
}

type ProgressFunc func(ProgressEvent)

func reportProgress(progress ProgressFunc, event ProgressEvent) {
	if progress == nil {
		return
	}
	event.Agent = strings.TrimSpace(event.Agent)
	event.TaskID = strings.TrimSpace(event.TaskID)
	event.ParentID = strings.TrimSpace(event.ParentID)
	event.Action = strings.TrimSpace(event.Action)
	event.Detail = strings.TrimSpace(event.Detail)
	progress(event)
}
