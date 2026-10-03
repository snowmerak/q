package memory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/snowmerak/q/client"
)

const (
	SetActiveWorkTool = "memory_set_active_work"
	CompleteWorkTool  = "memory_complete_work"
	RecordFactTool    = "memory_record_fact"
	CheckpointTool    = "memory_checkpoint"
	maximumMemoryText = 2048
)

type memoryEntry struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type memoryDelta struct {
	EventID   string       `json:"event_id,omitempty"`
	Revision  uint64       `json:"revision,omitempty"`
	Operation string       `json:"operation"`
	Entry     memoryEntry  `json:"entry"`
	Next      *memoryEntry `json:"next,omitempty"`
	Coverage  string       `json:"coverage_digest,omitempty"`
}

// taskMemory is rebuilt from checkpoint and successful tool results whenever a
// loop resumes. Tool replies contain only a delta, so repeated updates do not
// copy the entire checkpoint into the provider context.
type taskMemory struct {
	revision        uint64
	versioned       bool
	currentRequest  []string
	active          []memoryEntry
	previous        []memoryEntry
	facts           []memoryEntry
	touchedActive   bool
	touchedPrevious bool
	touchedFacts    bool
}

// MemoryTools are available in every Q model loop. They update the current
// loop's continuation checkpoint and never perform workspace actions.
func MemoryTools() []client.Tool {
	stringField := func() map[string]any { return map[string]any{"type": "string", "maxLength": maximumMemoryText} }
	tool := func(name, description string, properties map[string]any, required ...string) client.Tool {
		properties["expected_revision"] = map[string]any{"type": "integer", "minimum": 0, "description": "Revision from the latest successful memory result. A stale revision is rejected."}
		return client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
			Name: name, Description: description,
			Parameters: map[string]any{
				"type": "object", "properties": properties, "required": required,
				"additionalProperties": false,
			},
		}}
	}
	return []client.Tool{
		tool(SetActiveWorkTool, "Record or update one pending task in this session's active work. Use it when a small task or next action becomes clear.",
			map[string]any{"work_id": stringField(), "description": stringField(), "next_action": stringField()}, "description"),
		tool(CompleteWorkTool, "Move one completed small task from active work to previous work. Optionally register the next task in the same update.",
			map[string]any{"work_id": stringField(), "result": stringField(), "next_work": stringField()}, "work_id", "result"),
		tool(RecordFactTool, "Record or correct one confirmed fact for continuing this session. Include a source reference when available.",
			map[string]any{"fact_id": stringField(), "fact": stringField(), "source": stringField()}, "fact"),
		tool(CheckpointTool, "Confirm that the maintained task memory captures all durable outcomes, constraints, unresolved work and evidence references in the conversation so far. First record any missing work or facts. Call this tool ALONE, with the latest memory revision, at useful work boundaries. This allows covered history to be compacted without summarizing it again; original user requests are retained by the host. Do not confirm incomplete memory.",
			map[string]any{}, "expected_revision"),
	}
}

func IsMemoryTool(name string) bool {
	switch name {
	case SetActiveWorkTool, CompleteWorkTool, RecordFactTool, CheckpointTool:
		return true
	default:
		return false
	}
}

// AppendMemoryTools adds the built-in memory tools without duplicating a name
// already present in a caller's tool catalog.
func AppendMemoryTools(tools []client.Tool) []client.Tool {
	result := make([]client.Tool, 0, len(tools)+4)
	for _, tool := range tools {
		if !IsMemoryTool(tool.Function.Name) {
			result = append(result, tool)
		}
	}
	result = append(result, MemoryTools()...)
	return result
}

func parseMemoryArguments(arguments string, output any) error {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func memoryText(value, name string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if len(value) > maximumMemoryText {
		return "", fmt.Errorf("%s exceeds %d bytes", name, maximumMemoryText)
	}
	return value, nil
}

func newMemoryID(prefix string) (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(random[:]), nil
}

func findMemoryEntry(entries []memoryEntry, id string) int {
	for index, entry := range entries {
		if entry.ID == id {
			return index
		}
	}
	return -1
}

func setMemoryEntry(entries []memoryEntry, entry memoryEntry) []memoryEntry {
	if index := findMemoryEntry(entries, entry.ID); index >= 0 {
		entries[index] = entry
		return entries
	}
	return append(entries, entry)
}

func (s *taskMemory) apply(delta memoryDelta) bool {
	if delta.Revision > 0 {
		// Results are applied once when called and again when appended. Older
		// retained results can also follow a newer checkpoint after compaction.
		if delta.EventID == "" || delta.Revision != s.revision+1 {
			return false
		}
	} else if s.versioned {
		return false
	}
	switch delta.Operation {
	case "set_active":
		s.active = setMemoryEntry(s.active, delta.Entry)
		s.touchedActive = true
	case "complete":
		if index := findMemoryEntry(s.active, delta.Entry.ID); index >= 0 {
			s.active = append(s.active[:index], s.active[index+1:]...)
		}
		s.previous = setMemoryEntry(s.previous, delta.Entry)
		if delta.Next != nil {
			s.active = setMemoryEntry(s.active, *delta.Next)
		}
		s.touchedActive, s.touchedPrevious = true, true
	case "record_fact":
		s.facts = setMemoryEntry(s.facts, delta.Entry)
		s.touchedFacts = true
	case "checkpoint":
		if delta.Coverage == "" || delta.Revision == 0 {
			return false
		}
	default:
		return false
	}
	if delta.Revision > 0 {
		s.revision = delta.Revision
		s.versioned = true
	}
	return true
}

type revisionInput struct {
	ExpectedRevision *uint64 `json:"expected_revision,omitempty"`
}

func (s *taskMemory) call(call client.ToolCall, coverage string) (client.ToolResult, error) {
	var revision revisionInput
	if err := json.Unmarshal([]byte(call.Function.Arguments), &revision); err != nil {
		return client.ToolResult{}, err
	}
	if revision.ExpectedRevision != nil && *revision.ExpectedRevision != s.revision {
		return client.ToolResult{}, fmt.Errorf("stale memory revision: got %d, current revision is %d", *revision.ExpectedRevision, s.revision)
	}
	var delta memoryDelta
	switch call.Function.Name {
	case SetActiveWorkTool:
		var input struct {
			revisionInput
			WorkID      string `json:"work_id"`
			Description string `json:"description"`
			NextAction  string `json:"next_action"`
		}
		if err := parseMemoryArguments(call.Function.Arguments, &input); err != nil {
			return client.ToolResult{}, err
		}
		description, err := memoryText(input.Description, "description")
		if err != nil {
			return client.ToolResult{}, err
		}
		id := strings.TrimSpace(input.WorkID)
		if id == "" {
			id, err = newMemoryID("w-")
			if err != nil {
				return client.ToolResult{}, err
			}
		} else if findMemoryEntry(s.active, id) < 0 {
			return client.ToolResult{}, fmt.Errorf("active work %q does not exist", id)
		}
		text := description
		if next := strings.TrimSpace(input.NextAction); next != "" {
			if len(next) > maximumMemoryText {
				return client.ToolResult{}, errors.New("next_action is too long")
			}
			text += "; next: " + next
		}
		delta = memoryDelta{Operation: "set_active", Entry: memoryEntry{ID: id, Text: text}}
	case CompleteWorkTool:
		var input struct {
			revisionInput
			WorkID   string `json:"work_id"`
			Result   string `json:"result"`
			NextWork string `json:"next_work"`
		}
		if err := parseMemoryArguments(call.Function.Arguments, &input); err != nil {
			return client.ToolResult{}, err
		}
		id := strings.TrimSpace(input.WorkID)
		index := findMemoryEntry(s.active, id)
		if index < 0 {
			return client.ToolResult{}, fmt.Errorf("active work %q does not exist", id)
		}
		result, err := memoryText(input.Result, "result")
		if err != nil {
			return client.ToolResult{}, err
		}
		delta = memoryDelta{Operation: "complete", Entry: memoryEntry{ID: id, Text: result}}
		if next := strings.TrimSpace(input.NextWork); next != "" {
			if len(next) > maximumMemoryText {
				return client.ToolResult{}, errors.New("next_work is too long")
			}
			nextID, err := newMemoryID("w-")
			if err != nil {
				return client.ToolResult{}, err
			}
			delta.Next = &memoryEntry{ID: nextID, Text: next}
		}
	case RecordFactTool:
		var input struct {
			revisionInput
			FactID string `json:"fact_id"`
			Fact   string `json:"fact"`
			Source string `json:"source"`
		}
		if err := parseMemoryArguments(call.Function.Arguments, &input); err != nil {
			return client.ToolResult{}, err
		}
		fact, err := memoryText(input.Fact, "fact")
		if err != nil {
			return client.ToolResult{}, err
		}
		id := strings.TrimSpace(input.FactID)
		if id == "" {
			id, err = newMemoryID("f-")
			if err != nil {
				return client.ToolResult{}, err
			}
		} else if findMemoryEntry(s.facts, id) < 0 {
			return client.ToolResult{}, fmt.Errorf("fact %q does not exist", id)
		}
		if source := strings.TrimSpace(input.Source); source != "" {
			if len(source) > maximumMemoryText {
				return client.ToolResult{}, errors.New("source is too long")
			}
			fact += "; source: " + source
		}
		delta = memoryDelta{Operation: "record_fact", Entry: memoryEntry{ID: id, Text: fact}}
	case CheckpointTool:
		if err := parseMemoryArguments(call.Function.Arguments, &revision); err != nil {
			return client.ToolResult{}, err
		}
		if revision.ExpectedRevision == nil || coverage == "" {
			return client.ToolResult{}, errors.New("checkpoint requires expected_revision and a standalone call after completed tool exchanges")
		}
		if !checkpointHasContent(s.checkpoint()) {
			return client.ToolResult{}, errors.New("record task memory before confirming coverage")
		}
		delta = memoryDelta{Operation: "checkpoint", Coverage: coverage}
	default:
		return client.ToolResult{}, fmt.Errorf("unknown memory tool %q", call.Function.Name)
	}
	eventID, err := newMemoryID("e-")
	if err != nil {
		return client.ToolResult{}, err
	}
	delta.EventID, delta.Revision = eventID, s.revision+1
	body, err := json.Marshal(delta)
	if err != nil {
		return client.ToolResult{}, err
	}
	s.apply(delta)
	return client.ToolResult{Content: string(body)}, nil
}

func (s *taskMemory) replay(messages []client.Message) {
	for _, message := range messages {
		if message.Name == SummaryName {
			if update, err := parseCheckpointUpdate(message.Content); err == nil {
				s.loadCheckpoint(update.checkpoint)
				if metadata, ok := storedMemoryMetadata(message.Content); ok {
					s.revision, s.versioned = metadata.Revision, true
				}
			}
			continue
		}
		if message.Role != client.RoleTool || !IsMemoryTool(message.Name) {
			continue
		}
		var delta memoryDelta
		if err := json.Unmarshal([]byte(message.Content), &delta); err != nil {
			continue
		}
		if delta.Operation != "checkpoint" && (delta.Entry.ID == "" || delta.Entry.Text == "") {
			continue
		}
		s.apply(delta)
	}
}

func parseMemoryLine(line, prefix string, index int) memoryEntry {
	line = strings.TrimSpace(line)
	if id, text, ok := strings.Cut(line, ": "); ok && (strings.HasPrefix(id, prefix) || strings.HasPrefix(id, "legacy-"+prefix)) {
		return memoryEntry{ID: id, Text: text}
	}
	return memoryEntry{ID: fmt.Sprintf("legacy-%s-%d", prefix, index), Text: line}
}

func (s *taskMemory) loadCheckpoint(checkpoint Checkpoint) {
	s.revision, s.versioned = 0, false
	s.currentRequest = append([]string(nil), checkpoint.CurrentRequest...)
	s.active, s.previous, s.facts = nil, nil, nil
	for index, line := range checkpoint.ActiveWork {
		s.active = append(s.active, parseMemoryLine(line, "w-", index))
	}
	for index, line := range checkpoint.PreviousWork {
		s.previous = append(s.previous, parseMemoryLine(line, "w-", index))
	}
	for index, line := range checkpoint.Facts {
		s.facts = append(s.facts, parseMemoryLine(line, "f-", index))
	}
	s.touchedActive, s.touchedPrevious, s.touchedFacts = false, false, false
}

func renderMemoryEntries(entries []memoryEntry) []string {
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.ID+": "+entry.Text)
	}
	return result
}

func (s *taskMemory) overlay(checkpoint Checkpoint) Checkpoint {
	if s.touchedActive {
		checkpoint.ActiveWork = mergeMemoryEntries(checkpoint.ActiveWork, s.active, s.previous)
	}
	if s.touchedPrevious {
		checkpoint.PreviousWork = mergeMemoryEntries(checkpoint.PreviousWork, s.previous, nil)
	}
	if s.touchedFacts {
		checkpoint.Facts = mergeMemoryEntries(checkpoint.Facts, s.facts, nil)
	}
	return checkpoint
}

func (s *taskMemory) checkpoint() Checkpoint {
	return Checkpoint{CurrentRequest: append([]string(nil), s.currentRequest...),
		ActiveWork: renderMemoryEntries(s.active), PreviousWork: renderMemoryEntries(s.previous), Facts: renderMemoryEntries(s.facts)}
}

// Keep newly summarized, unrecorded evidence as well as authoritative entries.
// A completed ID must not be resurrected by a stale summary or retained delta.
func mergeMemoryEntries(lines []string, entries, removed []memoryEntry) []string {
	result := renderMemoryEntries(entries)
	texts := make(map[string]bool, len(entries)+len(removed))
	for _, entry := range entries {
		texts[entry.Text] = true
	}
	for _, entry := range removed {
		texts[entry.Text] = true
	}
	for _, line := range lines {
		id, _, _ := strings.Cut(line, ": ")
		if !texts[line] && findMemoryEntry(entries, id) < 0 && findMemoryEntry(removed, id) < 0 {
			result = append(result, line)
		}
	}
	return compactCheckpointItems(result)
}
