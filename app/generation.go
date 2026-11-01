package app

import (
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/sessionstore"
)

// Retain partial text only in the visible/audit transcript. It must not become
// model context or an executable tool call on a later user turn.
func (m *model) retainIncompleteGeneration(reason, content string, usage client.Usage) {
	if strings.TrimSpace(content) == "" {
		return
	}
	message := client.Message{Role: client.RoleAssistant, Name: "q_incomplete_generation", Content: "[Incomplete response: " + reason + "]\n" + content}
	m.messages = append(m.messages, message)
	m.recordResponseUsage(usage)
	status := sessionstore.StatusFailed
	if reason == "cancelled" {
		status = sessionstore.StatusCancelled
	}
	m.archiveMessage(message, status, true)
	m.appendAgentTrace(agentTrace{Agent: "model", Kind: "incomplete_generation", Name: reason, Content: content, IsError: true})
}
