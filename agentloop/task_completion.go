package agentloop

import (
	"strings"

	"github.com/snowmerak/q/client"
)

const terminalAnswerInstruction = "The host has accepted this terminal tool result. The task is finished. " +
	"Provide the final answer to the user's original request using the evidence and results already collected. " +
	"Include the requested findings, explanation, source references and limitations as appropriate. " +
	"Do not merely acknowledge completion or describe what you did. Do not call any more tools."

// Call sites only consider plain responses or a validated task_complete turn.
// Content attached to other tool calls must never suppress the final answer.
func isUserAnswer(message client.Message) bool {
	return message.Phase != "commentary" && strings.TrimSpace(message.TextContent()) != ""
}
