package agentloop

import (
	"strings"

	"github.com/snowmerak/q/client"
)

// ProviderMessages strips host metadata and optionally coalesces instructions.
func ProviderMessages(messages []client.Message, coalesceInstructions bool) []client.Message {
	return providerMessages(messages, coalesceInstructions)
}

// ProviderSafeContentParts removes metadata that cannot be sent to a provider.
func ProviderSafeContentParts(parts []client.MessageContentPart) []client.MessageContentPart {
	return providerSafeContentParts(parts)
}

// providerMessages removes q-internal message names that some compatible APIs
// reject outside the user role. For OpenAI-compatible routes it also combines
// the leading system/developer instruction block because strict local chat
// templates commonly treat every developer message as a system message while
// allowing a system message only at the beginning.
func providerMessages(messages []client.Message, coalesceInstructions bool) []client.Message {
	result := append([]client.Message(nil), messages...)
	for index := range result {
		result[index].ContentParts = providerSafeContentParts(result[index].ContentParts)
		if result[index].Role != client.RoleUser {
			result[index].Name = ""
		}
	}
	if !coalesceInstructions {
		return result
	}
	leading := 0
	var instructions []string
	for leading < len(result) && (result[leading].Role == client.RoleSystem || result[leading].Role == client.RoleDeveloper) {
		if content := strings.TrimSpace(result[leading].TextContent()); content != "" {
			instructions = append(instructions, content)
		}
		leading++
	}
	if leading == 0 {
		return result
	}
	merged := client.Message{Role: client.RoleSystem, Content: strings.Join(instructions, "\n\n")}
	coalesced := make([]client.Message, 0, len(result)-leading+1)
	coalesced = append(coalesced, merged)
	return append(coalesced, result[leading:]...)
}

// providerSafeContentParts removes host metadata before sending a request to a provider.
func providerSafeContentParts(parts []client.MessageContentPart) []client.MessageContentPart {
	if len(parts) == 0 {
		return nil
	}
	result := make([]client.MessageContentPart, 0, len(parts))
	for _, part := range parts {
		clean := make(client.MessageContentPart, len(part))
		for key, value := range part {
			if key != "q_acp_content_block" {
				clean[key] = value
			}
		}
		if len(clean) > 0 {
			result = append(result, clean)
		}
	}
	return result
}
