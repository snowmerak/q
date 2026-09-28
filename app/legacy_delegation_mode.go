package app

import "github.com/snowmerak/q/client"

const (
	legacyDelegationPromptName = "q_delegation_mode"
	legacyDelegationPolicyName = "q_delegation_policy"
)

// withoutLegacyDelegationModeMessages prevents sessions created before the
// mode was removed from restoring its coordinator-only instructions.
func withoutLegacyDelegationModeMessages(messages []client.Message) []client.Message {
	result := make([]client.Message, 0, len(messages))
	for _, message := range messages {
		if message.Name != legacyDelegationPromptName && message.Name != legacyDelegationPolicyName {
			result = append(result, message)
		}
	}
	return result
}
