package subagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/sessionstore"
)

// RecordSink is implemented by sessionstore.Writer and small test doubles.
type RecordSink interface {
	Append(sessionstore.Record) error
}

// Lifecycle writes one subagent task and its messages/results into the shared
// workspace archive. It is ready to be attached to the subagent runner when
// that execution layer is introduced.
type Lifecycle struct {
	sink     RecordSink
	runID    string
	taskID   string
	parentID string
	spec     *Spec
	prompt   string
}

func NewLifecycle(sink RecordSink, runID, taskID, parentID string, spec *Spec) (*Lifecycle, error) {
	if sink == nil {
		return nil, errors.New("subagent: record sink is required")
	}
	if spec == nil {
		return nil, errors.New("subagent: model spec is required")
	}
	if strings.TrimSpace(runID) == "" {
		return nil, errors.New("subagent: run ID is required")
	}
	if strings.TrimSpace(taskID) == "" {
		id, err := sessionstore.NewID()
		if err != nil {
			return nil, err
		}
		taskID = "task-" + id
	}
	return &Lifecycle{
		sink: sink, runID: runID, taskID: taskID, parentID: parentID, spec: spec,
	}, nil
}

func (l *Lifecycle) TaskID() string { return l.taskID }

func (l *Lifecycle) Queued(prompt string) error {
	l.prompt = prompt
	return l.writeTask(sessionstore.StatusQueued, "")
}

func (l *Lifecycle) Started(prompt string) error {
	if prompt != "" {
		l.prompt = prompt
	}
	return l.writeTask(sessionstore.StatusRunning, "")
}

func (l *Lifecycle) Message(message client.Message) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	kind := sessionstore.KindMessage
	role := string(message.Role)
	status := sessionstore.StatusSucceeded
	if message.Role == client.RoleAssistant {
		role = l.spec.Role
	}
	if message.Role == client.RoleTool {
		kind = sessionstore.KindResult
	}
	return l.sink.Append(sessionstore.Record{
		Kind: kind, RunID: l.runID, TaskID: l.taskID, ParentID: l.taskRecordID(),
		Role: role, Model: l.spec.Model, Effort: l.spec.ReasoningEffort, Status: status,
		Content: message.TextContent(), Tags: []string{"subagent", l.spec.Role}, Payload: payload,
	})
}

func (l *Lifecycle) Succeeded(summary, content string, payload any) error {
	return l.finish(sessionstore.StatusSucceeded, summary, content, payload)
}

func (l *Lifecycle) Failed(err error) error {
	if err == nil {
		return errors.New("subagent: failure error is required")
	}
	return l.finish(sessionstore.StatusFailed, err.Error(), err.Error(), map[string]string{"error": err.Error()})
}

func (l *Lifecycle) Cancelled(summary string) error {
	return l.finish(sessionstore.StatusCancelled, summary, "", nil)
}

func (l *Lifecycle) finish(status, summary, content string, value any) error {
	if err := l.writeTask(status, summary); err != nil {
		return err
	}
	payload, err := marshalLifecyclePayload(value)
	if err != nil {
		return err
	}
	return l.sink.Append(sessionstore.Record{
		ID: l.resultRecordID(), Kind: sessionstore.KindResult,
		RunID: l.runID, TaskID: l.taskID, ParentID: l.taskRecordID(),
		Role: l.spec.Role, Model: l.spec.Model, Effort: l.spec.ReasoningEffort, Status: status,
		Summary: summary, Content: content, Tags: []string{"subagent", l.spec.Role}, Payload: payload,
	})
}

func (l *Lifecycle) writeTask(status, summary string) error {
	payload, err := json.Marshal(map[string]string{
		"role": l.spec.Role, "model": l.spec.Model, "reasoning_effort": l.spec.ReasoningEffort,
	})
	if err != nil {
		return err
	}
	return l.sink.Append(sessionstore.Record{
		ID: l.taskRecordID(), Kind: sessionstore.KindTask,
		RunID: l.runID, TaskID: l.taskID, ParentID: l.parentID,
		Role: l.spec.Role, Model: l.spec.Model, Effort: l.spec.ReasoningEffort, Status: status,
		Summary: summary, Content: l.prompt, Tags: []string{"subagent", l.spec.Role}, Payload: payload,
	})
}

func (l *Lifecycle) taskRecordID() string {
	return lifecycleRecordID("subagent-task", l.runID, l.taskID)
}

func (l *Lifecycle) resultRecordID() string {
	return lifecycleRecordID("subagent-result", l.runID, l.taskID)
}

func lifecycleRecordID(prefix, runID, taskID string) string {
	sum := sha256.Sum256([]byte(runID + "\x00" + taskID))
	return prefix + "-" + hex.EncodeToString(sum[:])
}

func marshalLifecyclePayload(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	return json.Marshal(value)
}
