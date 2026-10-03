package app

import (
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/workspace"
)

func responseTokenUsage(usage client.Usage) *workspace.TokenUsage {
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 && usage.PromptDetails == nil {
		return nil
	}
	result := &workspace.TokenUsage{InputTokens: max(0, usage.PromptTokens), OutputTokens: max(0, usage.CompletionTokens)}
	if usage.PromptDetails != nil {
		cached := max(0, usage.PromptDetails.CachedTokens)
		result.CachedTokens = &cached
	}
	return result
}

func (m *model) recordResponseUsage(usage client.Usage) {
	counts := responseTokenUsage(usage)
	if counts == nil {
		return
	}
	ordinal := -1
	for _, message := range m.messages {
		if message.Role == client.RoleAssistant {
			ordinal++
		}
	}
	if ordinal >= 0 {
		m.responseUsage = append(m.responseUsage, workspace.ResponseUsage{AssistantIndex: ordinal, TokenUsage: *counts})
	}
}
