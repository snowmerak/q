package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/sessionstore"
)

type recordArchive interface {
	Append(sessionstore.Record) error
	Flush() error
}

type archivedMessagePayload struct {
	ConversationID string         `json:"conversation_id,omitempty"`
	Message        client.Message `json:"message"`
	IsError        bool           `json:"is_error,omitempty"`
}

func (m *model) ensureRunID() {
	if strings.TrimSpace(m.runID) != "" {
		return
	}
	id, err := sessionstore.NewID()
	if err != nil {
		id = fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	m.runID = "run-" + id
}

func (m *model) archiveMessage(message client.Message, status string, isError bool) {
	if m.archive == nil {
		return
	}
	m.ensureRunID()
	kind := sessionstore.KindMessage
	tags := []string{"chat"}
	content := message.TextContent()
	payloadMessage := message
	parentID := ""
	taskID := ""
	refs := make([]string, 0, len(message.ToolCalls))
	if message.Role == client.RoleTool {
		kind = sessionstore.KindResult
		tags = append(tags, "tool")
		taskID = message.ToolCallID
		parentID = toolCallRecordID(m.runID, message.ToolCallID)
		if isArchiveReadTool(message.Name) && !isError {
			// Archive reads are already durable. Keep invocation/result metadata
			// without recursively copying retrieved records back into the index.
			content = ""
			payloadMessage.Content = ""
			tags = append(tags, "archive-read")
		}
	}
	for _, call := range message.ToolCalls {
		refs = append(refs, toolCallRecordID(m.runID, call.ID))
	}
	model := ""
	if message.Role == client.RoleAssistant {
		model = m.activeModel()
	}
	payload, err := json.Marshal(archivedMessagePayload{
		ConversationID: m.conversationID,
		Message:        payloadMessage,
		IsError:        isError,
	})
	if err != nil {
		m.rememberArchiveError(err)
		return
	}
	refs = append(refs, sessionstore.ExtractLoomReferences(content, string(payload))...)
	record := sessionstore.Record{
		Kind: kind, RunID: m.runID, TaskID: taskID, ParentID: parentID,
		Role: string(message.Role), Model: model, Status: status,
		Content: content, Refs: refs, Tags: tags, Payload: payload,
	}
	if message.Role == client.RoleTool {
		record.Summary = message.Name
	}
	m.appendArchive(record)
}

func (m *model) archiveToolCall(call client.ToolCall) {
	if m.archive == nil {
		return
	}
	m.ensureRunID()
	payload, err := json.Marshal(call)
	if err != nil {
		m.rememberArchiveError(err)
		return
	}
	content := call.Function.Arguments
	tags := []string{"chat", "tool", "call"}
	kind := sessionstore.KindEvent
	if call.Function.Name == askToUserToolName {
		kind = sessionstore.KindQuestion
		tags = append(tags, "question")
	}
	if isArchiveReadTool(call.Function.Name) {
		content = ""
		tags = append(tags, "archive-read")
	}
	m.appendArchive(sessionstore.Record{
		ID: toolCallRecordID(m.runID, call.ID), Kind: kind,
		RunID: m.runID, TaskID: call.ID, Role: "tool", Status: sessionstore.StatusRunning,
		Summary: call.Function.Name, Content: content, Tags: tags, Payload: payload,
	})
}

func (m *model) archiveFailure(stage string, err error) {
	if m.archive == nil || err == nil {
		return
	}
	m.ensureRunID()
	payload, marshalErr := json.Marshal(map[string]string{"stage": stage, "error": err.Error()})
	if marshalErr != nil {
		m.rememberArchiveError(marshalErr)
		return
	}
	m.appendArchive(sessionstore.Record{
		Kind: sessionstore.KindEvent, RunID: m.runID, Model: m.activeModel(),
		Status: sessionstore.StatusFailed, Summary: stage, Content: err.Error(),
		Tags: []string{"chat", "error"}, Payload: payload,
	})
}

func (m *model) archiveTurnCancelled(reason string) {
	if m.archive == nil {
		return
	}
	m.ensureRunID()
	payload, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		m.rememberArchiveError(err)
		return
	}
	m.appendArchive(sessionstore.Record{
		Kind: sessionstore.KindEvent, RunID: m.runID, Model: m.activeModel(),
		Status: sessionstore.StatusCancelled, Summary: "turn interrupted", Content: reason,
		Tags: []string{"chat", "turn", "cancelled"}, Payload: payload,
	})
}

func (m *model) archiveSummary(content string) {
	if m.archive == nil || strings.TrimSpace(content) == "" {
		return
	}
	m.ensureRunID()
	m.appendArchive(sessionstore.Record{
		Kind: sessionstore.KindSummary, RunID: m.runID, Role: "assistant",
		Model: m.activeModel(), Status: sessionstore.StatusSucceeded,
		Summary: "context compaction", Content: content, Tags: []string{"chat", "compaction"},
	})
}

func (m *model) appendArchive(record sessionstore.Record) {
	if m.archive == nil {
		return
	}
	if err := m.archive.Append(record); err != nil {
		m.rememberArchiveError(err)
	}
}

func (m *model) rememberArchiveError(err error) {
	if err != nil {
		m.archiveErr = errors.Join(m.archiveErr, err)
	}
}

func (m *model) flushArchive() error {
	if m.archive == nil {
		return m.archiveErr
	}
	return errors.Join(m.archiveErr, m.archive.Flush())
}

func toolCallRecordID(runID, callID string) string {
	sum := sha256.Sum256([]byte(runID + "\x00" + callID))
	return "tool-call-" + hex.EncodeToString(sum[:])
}

func isArchiveReadTool(name string) bool {
	return name == "search_archive" || name == "get_archive_record"
}
