package memory

import (
	"strings"

	"github.com/snowmerak/q/client"
)

const (
	detachedCheckpointPrefix = "Update the session continuation checkpoint from the supplied conversation using no more than "
	inPrefixCheckpointPrefix = "Context compaction requested. Do NOT call any tools."
)

// CheckpointRequestKind describes how a request asks for a continuation checkpoint.
type CheckpointRequestKind int

const (
	CheckpointNone CheckpointRequestKind = iota
	CheckpointInPrefix
	CheckpointDetached
)

// ClassifyCheckpointRequest recognizes the prompts built by Plan in their
// expected message positions. In-prefix compaction can reuse an empty catalog.
func ClassifyCheckpointRequest(request client.ChatRequest) CheckpointRequestKind {
	messages := request.Messages
	if len(messages) == 0 {
		return CheckpointNone
	}
	if first := messages[0]; first.Role == client.RoleSystem &&
		len(request.Tools) == 0 && request.ConversationID == "" &&
		strings.HasPrefix(first.Content, detachedCheckpointPrefix) {
		return CheckpointDetached
	}
	if last := messages[len(messages)-1]; last.Role == client.RoleUser &&
		strings.HasPrefix(last.Content, inPrefixCheckpointPrefix+"\n") {
		return CheckpointInPrefix
	}
	return CheckpointNone
}
