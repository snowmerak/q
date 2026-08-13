package thinker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
)

const (
	LearningStateVersion    = 1
	TaskCompleteEventName   = "q_thinker_task_complete"
	PlanApprovedEventName   = "q_thinker_plan_approved"
	TaskCompletionReplyName = "q_task_complete_reply"

	BoundarySize         = "size"
	BoundaryTaskComplete = "task_complete"
	BoundaryPlanApproved = "plan_approved"
	BoundaryExplicit     = "explicit"
)

// LearningState is the durable cursor, uncommitted event log, and FIFO segment
// queue for one workspace conversation. Events at or below CommittedCursor
// have completed extraction and may be pruned.
type LearningState struct {
	Version         int               `json:"version"`
	StreamID        string            `json:"stream_id"`
	NextCursor      uint64            `json:"next_cursor"`
	CommittedCursor uint64            `json:"committed_cursor,omitempty"`
	Events          []LearningEvent   `json:"events,omitempty"`
	Queue           []LearningSegment `json:"queue,omitempty"`
}

type LearningEvent struct {
	Cursor  uint64         `json:"cursor"`
	Message client.Message `json:"message"`
}

type LearningSegment struct {
	ID          string `json:"id"`
	StartCursor uint64 `json:"start_cursor"`
	EndCursor   uint64 `json:"end_cursor"`
	Reason      string `json:"reason"`
}

// Machine deterministically closes semantic conversation segments. It does
// not run models; callers persist State and execute Queue in order.
type Machine struct {
	state         LearningState
	contextLength int64
}

func NewMachine(state LearningState, contextLength int64) (*Machine, error) {
	if state.Version == 0 {
		state.Version = LearningStateVersion
	}
	if state.Version != LearningStateVersion {
		return nil, fmt.Errorf("thinker: unsupported learning state version %d", state.Version)
	}
	if state.StreamID == "" {
		id, err := learningStreamID()
		if err != nil {
			return nil, err
		}
		state.StreamID = id
	}
	if state.NextCursor == 0 {
		state.NextCursor = 1
	}
	state = state.Clone()
	if err := validateLearningState(state); err != nil {
		return nil, err
	}
	return &Machine{state: state, contextLength: contextLength}, nil
}

func (s LearningState) Clone() LearningState {
	result := s
	result.Events = make([]LearningEvent, len(s.Events))
	for index, event := range s.Events {
		result.Events[index] = event
		result.Events[index].Message.ToolCalls = append([]client.ToolCall(nil), event.Message.ToolCalls...)
	}
	result.Queue = append([]LearningSegment(nil), s.Queue...)
	return result
}

func (m *Machine) State() LearningState {
	if m == nil {
		return LearningState{}
	}
	return m.state.Clone()
}

func (m *Machine) SetContextLength(contextLength int64) []LearningSegment {
	if m == nil {
		return nil
	}
	m.contextLength = contextLength
	return m.closeOversizedActive()
}

// Initialize seeds an empty state from the latest compacted summary when one
// exists, otherwise from the first user message. Existing state is untouched.
func (m *Machine) Initialize(messages []client.Message) []LearningSegment {
	if m == nil || len(m.state.Events) > 0 || len(m.state.Queue) > 0 || m.state.CommittedCursor > 0 {
		return nil
	}
	filtered := thinkerContextMessages(messages)
	start := -1
	for index := len(filtered) - 1; index >= 0; index-- {
		if filtered[index].Name == "q_context_summary" {
			start = index
			break
		}
	}
	if start < 0 {
		for index := range filtered {
			if filtered[index].Role == client.RoleUser {
				start = index
				break
			}
		}
	}
	if start < 0 {
		return nil
	}
	var closed []LearningSegment
	for _, message := range filtered[start:] {
		closed = append(closed, m.appendPrepared(message, "")...)
	}
	return closed
}

func (m *Machine) AppendMessage(message client.Message) []LearningSegment {
	prepared, ok := prepareThinkerContextMessage(message)
	if !ok {
		return nil
	}
	return m.appendPrepared(prepared, "")
}

func (m *Machine) AppendTaskComplete(result json.RawMessage) []LearningSegment {
	return m.appendSpecial(TaskCompleteEventName, result, BoundaryTaskComplete)
}

func (m *Machine) AppendPlanApproved(result json.RawMessage) []LearningSegment {
	return m.appendSpecial(PlanApprovedEventName, result, BoundaryPlanApproved)
}

// EnqueueExplicit closes the current segment. It intentionally reports no
// distinction for an empty segment so /learn and MCP learn remain fire-and-
// forget acknowledgements.
func (m *Machine) EnqueueExplicit() []LearningSegment {
	if m == nil || len(m.activeEvents()) == 0 {
		return nil
	}
	return []LearningSegment{m.closeActive(BoundaryExplicit)}
}

func (m *Machine) Next() (LearningSegment, []client.Message, bool) {
	if m == nil || len(m.state.Queue) == 0 {
		return LearningSegment{}, nil, false
	}
	segment := m.state.Queue[0]
	messages := make([]client.Message, 0)
	for _, event := range m.state.Events {
		if event.Cursor >= segment.StartCursor && event.Cursor <= segment.EndCursor {
			messages = append(messages, event.Message)
		}
	}
	if len(messages) == 0 {
		return LearningSegment{}, nil, false
	}
	return segment, messages, true
}

func (m *Machine) Commit(id string) error {
	if m == nil || len(m.state.Queue) == 0 {
		return errors.New("thinker: no learning segment is queued")
	}
	segment := m.state.Queue[0]
	if segment.ID != id {
		return fmt.Errorf("thinker: learning segment %q is not queue head %q", id, segment.ID)
	}
	m.state.CommittedCursor = segment.EndCursor
	m.state.Queue = append([]LearningSegment(nil), m.state.Queue[1:]...)
	first := 0
	for first < len(m.state.Events) && m.state.Events[first].Cursor <= m.state.CommittedCursor {
		first++
	}
	m.state.Events = append([]LearningEvent(nil), m.state.Events[first:]...)
	return nil
}

func (m *Machine) appendSpecial(name string, body json.RawMessage, reason string) []LearningSegment {
	if m == nil {
		return nil
	}
	if !json.Valid(body) {
		body, _ = json.Marshal(string(body))
	}
	message := client.Message{Role: client.RoleSystem, Name: name, Content: string(body)}
	return m.appendPrepared(message, reason)
}

func (m *Machine) appendPrepared(message client.Message, forcedReason string) []LearningSegment {
	if m == nil {
		return nil
	}
	var closed []LearningSegment
	active := m.activeEvents()
	if len(active) > 0 && m.contextLength > 0 {
		candidate := make([]client.Message, 0, len(active)+1)
		for _, event := range active {
			candidate = append(candidate, event.Message)
		}
		candidate = append(candidate, message)
		if contextPromptTokens(candidate, false) > contextMaximumTokens(m.contextLength) {
			closed = append(closed, m.closeActive(BoundarySize))
		}
	}
	event := LearningEvent{Cursor: m.state.NextCursor, Message: message}
	m.state.NextCursor++
	m.state.Events = append(m.state.Events, event)
	if forcedReason != "" {
		closed = append(closed, m.closeActive(forcedReason))
		return closed
	}
	if m.contextLength > 0 {
		active = m.activeEvents()
		messages := make([]client.Message, 0, len(active))
		for _, current := range active {
			messages = append(messages, current.Message)
		}
		if contextPromptTokens(messages, false) >= contextMaximumTokens(m.contextLength) {
			closed = append(closed, m.closeActive(BoundarySize))
		}
	}
	return closed
}

func (m *Machine) closeOversizedActive() []LearningSegment {
	if m == nil || m.contextLength <= 0 {
		return nil
	}
	active := m.activeEvents()
	if len(active) == 0 {
		return nil
	}
	messages := make([]client.Message, 0, len(active))
	for _, event := range active {
		messages = append(messages, event.Message)
	}
	if contextPromptTokens(messages, false) < contextMaximumTokens(m.contextLength) {
		return nil
	}
	return []LearningSegment{m.closeActive(BoundarySize)}
}

func (m *Machine) activeEvents() []LearningEvent {
	if m == nil {
		return nil
	}
	boundary := m.state.CommittedCursor
	if len(m.state.Queue) > 0 {
		boundary = m.state.Queue[len(m.state.Queue)-1].EndCursor
	}
	start := len(m.state.Events)
	for index, event := range m.state.Events {
		if event.Cursor > boundary {
			start = index
			break
		}
	}
	return m.state.Events[start:]
}

func (m *Machine) closeActive(reason string) LearningSegment {
	active := m.activeEvents()
	segment := LearningSegment{
		StartCursor: active[0].Cursor, EndCursor: active[len(active)-1].Cursor, Reason: reason,
	}
	segment.ID = fmt.Sprintf("learn-%s-%d-%d", m.state.StreamID, segment.StartCursor, segment.EndCursor)
	m.state.Queue = append(m.state.Queue, segment)
	return segment
}

func contextMaximumTokens(contextLength int64) int {
	return int(float64(contextLength) * ContextMaximumRatio)
}

func learningStreamID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("thinker: generate learning stream ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func validateLearningState(state LearningState) error {
	if strings.TrimSpace(state.StreamID) == "" || state.NextCursor == 0 {
		return errors.New("thinker: invalid learning state identity")
	}
	var previous uint64
	for _, event := range state.Events {
		if event.Cursor == 0 || event.Cursor <= previous || event.Cursor >= state.NextCursor {
			return errors.New("thinker: invalid learning event cursor")
		}
		previous = event.Cursor
	}
	previous = state.CommittedCursor
	for _, segment := range state.Queue {
		if segment.ID == "" || segment.StartCursor <= previous || segment.EndCursor < segment.StartCursor || segment.EndCursor >= state.NextCursor {
			return errors.New("thinker: invalid learning segment queue")
		}
		previous = segment.EndCursor
	}
	return nil
}
