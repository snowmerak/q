package memory

import (
	"encoding/json"
	"slices"

	"github.com/snowmerak/q/client"
)

// ReuseSkillResource replaces an unchanged get_skill body with a reference only
// when the complete resource is still in this context. Call after retrieving the
// current resource, and recheck after any pre-append compaction.
func ReuseSkillResource(message client.Message, context []client.Message) (client.Message, bool) {
	if message.Role != client.RoleTool || message.Name != skillToolName {
		return message, false
	}
	key, ok := skillResourceKey(message.TextContent())
	if !ok {
		return message, false
	}
	var current skillResourceVersion
	if json.Unmarshal([]byte(message.TextContent()), &current) != nil {
		return message, false
	}
	for _, previous := range slices.Backward(context) {
		if previous.Role != client.RoleTool || previous.Name != skillToolName || previous.ToolCallID == "" {
			continue
		}
		previousKey, ok := skillResourceKey(previous.TextContent())
		if !ok || previousKey != key {
			continue
		}
		var loaded skillResourceVersion
		if json.Unmarshal([]byte(previous.TextContent()), &loaded) != nil ||
			*current.Content != *loaded.Content || current.Skill.Digest != loaded.Skill.Digest {
			return message, false
		}
		body, _ := json.Marshal(struct {
			Skill              skillResourceIdentity `json:"skill"`
			Path               string                `json:"path"`
			AlreadyInContext   bool                  `json:"already_in_context"`
			PreviousToolCallID string                `json:"previous_tool_call_id"`
			Message            string                `json:"message"`
		}{current.Skill, current.Path, true, previous.ToolCallID,
			"This unchanged skill resource is already in the current context. Consult the earlier get_skill result identified by previous_tool_call_id and follow its complete text. Read it again only if it changes or is no longer in context."})
		message.Content, message.ContentParts = string(body), nil
		return message, true
	}
	return message, false
}

type skillResourceIdentity struct {
	ID     string `json:"id"`
	Digest string `json:"digest,omitempty"`
}

type skillResourceVersion struct {
	Skill   skillResourceIdentity `json:"skill"`
	Path    string                `json:"path"`
	Content *string               `json:"content"`
}
