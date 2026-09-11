package memory

import (
	"encoding/json"
	pathpkg "path"
	"slices"
	"strings"

	"github.com/snowmerak/q/client"
)

const (
	skillToolName       = "get_skill"
	skillRetentionRatio = 0.10
)

type skillResourceRead struct {
	key    string
	call   client.ToolCall
	result client.Message
}

// splitSkillResourceReads removes successful direct skill reads from ordinary
// summary/recent processing and selects a contiguous newest set under the
// independent retention policy. It only builds a compaction projection; the
// manager's append-only message prefix remains untouched until Apply.
func splitSkillResourceReads(messages []client.Message, immutablePrefix, contextWindow int) ([]client.Message, []client.Message) {
	if contextWindow <= 0 || len(messages) == 0 {
		return cloneMessages(messages), nil
	}
	immutablePrefix = min(max(immutablePrefix, 0), len(messages))
	results := make(map[string]client.Message)
	for index, message := range messages {
		if index < immutablePrefix || message.Role != client.RoleTool || message.Name != skillToolName || message.ToolCallID == "" {
			continue
		}
		if _, ok := skillResourceKey(message.Content); ok {
			results[message.ToolCallID] = message
		}
	}

	var reads []skillResourceRead
	removed := make(map[string]struct{})
	for index, message := range messages {
		if index < immutablePrefix || message.Role != client.RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.Function.Name != skillToolName {
				continue
			}
			result, ok := results[call.ID]
			if !ok {
				continue
			}
			key, _ := skillResourceKey(result.Content)
			reads = append(reads, skillResourceRead{key: key, call: call, result: result})
			removed[call.ID] = struct{}{}
		}
	}
	if len(reads) == 0 {
		return cloneMessages(messages), nil
	}

	ordinary := make([]client.Message, 0, len(messages))
	for index, message := range messages {
		if index < immutablePrefix {
			ordinary = append(ordinary, message)
			continue
		}
		if message.Role == client.RoleTool {
			if _, found := removed[message.ToolCallID]; found {
				continue
			}
		}
		if message.Role == client.RoleAssistant && len(message.ToolCalls) > 0 {
			calls := make([]client.ToolCall, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				if _, found := removed[call.ID]; !found {
					calls = append(calls, call)
				}
			}
			if len(calls) != len(message.ToolCalls) {
				message.ToolCalls = calls
				if len(calls) == 0 && message.Content == "" && len(message.ContentParts) == 0 {
					continue
				}
			}
		}
		ordinary = append(ordinary, message)
	}
	return ordinary, retainRecentSkillResources(reads, contextWindow)
}

func retainRecentSkillResources(reads []skillResourceRead, contextWindow int) []client.Message {
	budget := max(1, int(float64(contextWindow)*skillRetentionRatio))
	seen := make(map[string]struct{}, len(reads))
	selected := make([]skillResourceRead, 0, len(reads))
	used := 0
	for index := len(reads) - 1; index >= 0; index-- {
		read := reads[index]
		if _, found := seen[read.key]; found {
			continue
		}
		seen[read.key] = struct{}{}
		messages := read.messages()
		cost := CountMessages(messages)
		if len(selected) > 0 && used+cost > budget {
			break
		}
		selected = append(selected, read)
		used += cost
	}
	slices.Reverse(selected)
	retained := make([]client.Message, 0, len(selected)*2)
	for _, read := range selected {
		retained = append(retained, read.messages()...)
	}
	return retained
}

func (r skillResourceRead) messages() []client.Message {
	return []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{r.call}},
		r.result,
	}
}

func skillResourceKey(content string) (string, bool) {
	var output struct {
		Skill struct {
			ID string `json:"id"`
		} `json:"skill"`
		Path    string  `json:"path"`
		Content *string `json:"content"`
	}
	if err := json.Unmarshal([]byte(content), &output); err != nil || output.Content == nil {
		return "", false
	}
	id := strings.TrimSpace(output.Skill.ID)
	resource := strings.ReplaceAll(strings.TrimSpace(output.Path), "\\", "/")
	if id == "" {
		return "", false
	}
	if resource == "" || resource == "." {
		resource = "SKILL.md"
	} else {
		resource = pathpkg.Clean(resource)
	}
	return id + "\x00" + resource, true
}
