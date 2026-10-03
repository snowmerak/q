package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/snowmerak/q/client"
)

// RequestAnchorName holds original user messages removed from an incremental
// compaction source. Keeping complete messages also preserves non-text parts.
const RequestAnchorName = "q_context_requests"

type memoryMetadata struct {
	Version  int    `json:"version"`
	Revision uint64 `json:"revision"`
}

func storedMemoryMetadata(content string) (memoryMetadata, bool) {
	var stored struct {
		Memory memoryMetadata `json:"_memory"`
	}
	err := json.Unmarshal([]byte(strings.TrimPrefix(content, checkpointHeading)), &stored)
	return stored.Memory, err == nil && stored.Memory.Version == 1
}

func encodeStoredCheckpoint(value Checkpoint, revision uint64) (string, error) {
	body, err := json.Marshal(struct {
		Checkpoint
		Memory memoryMetadata `json:"_memory"`
	}{value, memoryMetadata{Version: 1, Revision: revision}})
	return string(body), err
}

// Exclude provider-private replay data: switching models can clear it without
// changing the conversation covered by an acknowledgment.
func coverageDigest(messages []client.Message) string {
	visible := cloneMessages(messages)
	for index := range visible {
		visible[index].ResponseOutput = nil
		visible[index].ResponseModel = ""
	}
	body, err := json.Marshal(visible)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func completeToolHistory(messages []client.Message) bool {
	for start := 0; start < len(messages); start++ {
		message := messages[start]
		if message.Role == client.RoleTool {
			return false
		}
		if len(message.ToolCalls) == 0 {
			continue
		}
		pending := make(map[string]bool, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			if call.ID == "" || pending[call.ID] {
				return false
			}
			pending[call.ID] = true
		}
		for start+1 < len(messages) && messages[start+1].Role == client.RoleTool {
			start++
			id := messages[start].ToolCallID
			if !pending[id] {
				return false
			}
			delete(pending, id)
		}
		if len(pending) != 0 {
			return false
		}
	}
	return true
}

func (m *Manager) checkpointCoverage(call client.ToolCall) string {
	if len(m.messages) == 0 {
		return ""
	}
	last := m.messages[len(m.messages)-1]
	if last.Role != client.RoleAssistant || len(last.ToolCalls) != 1 || last.ToolCalls[0] != call ||
		!completeToolHistory(m.messages[:len(m.messages)-1]) {
		return ""
	}
	return coverageDigest(m.messages)
}

// Acknowledgments only cover the exact completed prefix they were issued for.
// Legacy, missing, stale or altered evidence falls back to full-source summary.
func (m *Manager) coveredCheckpoint() (Checkpoint, uint64, int) {
	var state taskMemory
	var checkpoint Checkpoint
	var revision uint64
	boundary := -1
	for index, message := range m.messages {
		before := state.revision
		state.replay([]client.Message{message})
		if message.Name == SummaryName {
			_, parseErr := parseCheckpointUpdate(message.Content)
			if _, ok := storedMemoryMetadata(message.Content); ok && parseErr == nil && checkpointHasContent(state.checkpoint()) {
				checkpoint, revision, boundary = state.checkpoint(), state.revision, index
			}
			continue
		}
		if message.Role != client.RoleTool || message.Name != CheckpointTool || index == 0 {
			continue
		}
		var delta memoryDelta
		if json.Unmarshal([]byte(message.Content), &delta) != nil || delta.Operation != "checkpoint" ||
			delta.Revision != before+1 || state.revision != delta.Revision {
			continue
		}
		assistant := m.messages[index-1]
		if assistant.Role != client.RoleAssistant || len(assistant.ToolCalls) != 1 ||
			assistant.ToolCalls[0].ID != message.ToolCallID || assistant.ToolCalls[0].Function.Name != CheckpointTool ||
			!completeToolHistory(m.messages[:index-1]) || delta.Coverage != coverageDigest(m.messages[:index]) {
			continue
		}
		checkpoint, revision, boundary = state.checkpoint(), state.revision, index
	}
	return checkpoint, revision, boundary
}

func (m *Manager) incrementalSource(plan *Plan, messages []client.Message, immutable []bool, recentStart int) {
	checkpoint, revision, rawBoundary := m.coveredCheckpoint()
	boundary := -1
	if rawBoundary >= 0 {
		marker := m.messages[rawBoundary]
		for index, message := range messages {
			if message.Name == marker.Name && message.Content == marker.Content && message.ToolCallID == marker.ToolCallID {
				boundary = index
				// A duplicated result later in history must not widen coverage.
				break
			}
		}
	}
	if boundary < 0 {
		return
	}
	originalSource := plan.Source
	plan.Source = nil
	for index, message := range messages {
		if immutable[index] || index >= recentStart {
			continue
		}
		if index <= boundary {
			plan.CoveredMessages++
		} else {
			plan.Source = append(plan.Source, message)
		}
	}
	if plan.CoveredMessages == 0 {
		return
	}
	plan.Maintained = &checkpoint
	plan.MaintainedRevision = revision
	preserveUserRequests(plan, originalSource)
}

func preserveUserRequests(plan *Plan, source []client.Message) {
	var requests []client.Message
	anchor := -1
	for index, message := range plan.Immutable {
		if message.Name == RequestAnchorName {
			// Only host-created, valid anchors are extended; malformed ones stay
			// pinned verbatim and cannot authorize dropping source messages.
			if json.Unmarshal([]byte(message.Content), &requests) == nil {
				anchor = index
			}
		}
	}
	for _, message := range source {
		if message.Role == client.RoleUser && message.Name != continuationName {
			requests = append(requests, message)
		}
	}
	if len(requests) == 0 {
		return
	}
	body, _ := json.Marshal(requests)
	message := client.Message{Role: client.RoleSystem, Name: RequestAnchorName, Content: string(body)}
	if anchor >= 0 {
		plan.Immutable[anchor] = message
	} else {
		plan.Immutable = append(plan.Immutable, message)
	}
}

// CheckpointWithoutModel projects maintained memory when no uncovered source
// remains. Callers still apply and persist it through the normal atomic path.
func (p Plan) CheckpointWithoutModel() (string, bool) {
	if p.Maintained == nil || len(p.Source) != 0 {
		return "", false
	}
	body, err := json.Marshal(p.Maintained)
	return string(body), err == nil
}
