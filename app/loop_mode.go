package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/subagent"
)

const (
	loopModeDefault      = "default"
	loopModeDelegation   = "delegation"
	delegationPromptName = "q_delegation_mode"
	delegationPolicyName = "q_delegation_policy"
)

const delegationSystemPrompt = `You are the coordinator in Q's delegation mode. Use delegate_list and delegate to assign substantive repository investigation, implementation, review, planning, or external research to an appropriate available subagent. Give each delegate the objective, relevant context, constraints, and completion criteria. Inspect its result and evidence, coordinate follow-up work when needed, and report the actual outcome to the user. You have no direct workspace file, shell, or LSP tools in this mode. Your remaining Q tools support coordination and retrieval of saved evidence and skills. You may answer simple conversational questions directly without starting a task. Once you start a task, task_complete with outcome succeeded requires a successful delegate or other available work tool result; delegate_list alone is insufficient. If no suitable work tool is available, report a genuine blocker. Do not claim that a delegate succeeded without checking its result. This mode is separate from Q's approval-gated /plan workflow.`

const delegationDeveloperPolicy = `Q delegation mode is active. Use a suitable Q subagent for workspace inspection, implementation, and review; use delegate_list to check who is available. The root cannot directly inspect or change workspace files or run shell commands. Its remaining Q tools support coordination and retrieval; subagent tool grants are controlled by each subagent's own definition. Provider-private tools, including Codex's internal node_repl, do not appear in Q's session record and do not satisfy a Q-delegated task. After task_start, call task_complete with outcome succeeded only after a Q delegate or directly available Q work tool returns successfully. If no suitable Q work tool is available, report the concrete blocker.`

// priorTaskAction derives evidence from the durable transcript, which remains
// complete even when the model-facing context has been compacted.
func priorTaskAction(messages []client.Message) bool {
	start := -1
	for index, message := range messages {
		if message.Role != client.RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.Function.Name == taskStartToolName {
				start = index
			}
		}
	}
	if start < 0 {
		return false
	}
	for _, message := range messages[start+1:] {
		if message.Role != client.RoleTool || strings.HasPrefix(message.TextContent(), "Tool error:") {
			continue
		}
		if memory.IsMemoryTool(message.Name) {
			continue
		}
		switch message.Name {
		case taskStartToolName, taskCompleteToolName, askToUserToolName, subagent.DelegateListToolName:
			continue
		default:
			return true
		}
	}
	return false
}

func normalizedLoopMode(mode string) string {
	if mode == loopModeDelegation {
		return loopModeDelegation
	}
	return loopModeDefault
}

func withLoopModePrompt(messages []client.Message, mode string) []client.Message {
	result := make([]client.Message, 0, len(messages)+1)
	for _, message := range messages {
		if message.Name != delegationPromptName && message.Name != delegationPolicyName {
			result = append(result, message)
		}
	}
	if mode != loopModeDelegation {
		return result
	}
	insert := 0
	for insert < len(result) && result[insert].Role == client.RoleSystem {
		insert++
	}
	result = append(result, client.Message{})
	copy(result[insert+1:], result[insert:])
	result[insert] = client.Message{Role: client.RoleSystem, Name: delegationPromptName, Content: delegationSystemPrompt}
	for insert < len(result) && (result[insert].Role == client.RoleSystem || result[insert].Role == client.RoleDeveloper) {
		insert++
	}
	result = append(result, client.Message{})
	copy(result[insert+1:], result[insert:])
	result[insert] = client.Message{Role: client.RoleDeveloper, Name: delegationPolicyName, Content: delegationDeveloperPolicy}
	return result
}

func (m *model) setLoopMode(mode string) error {
	if mode != loopModeDefault && mode != loopModeDelegation {
		return fmt.Errorf("unknown mode %q", mode)
	}
	if m.waiting || m.asking || m.compacting || m.delegationRecoveryPending || m.recoverDelegationTurn {
		return errors.New("finish the current turn before changing mode")
	}
	if normalizedLoopMode(m.loopMode) == mode {
		return nil
	}
	previousMode, previousMessages, previousConversationID := m.loopMode, m.messages, m.conversationID
	var previousContext []client.Message
	if m.memory != nil {
		previousContext = m.memory.Messages()
	}
	m.loopMode = mode
	m.messages = withLoopModePrompt(m.messages, mode)
	if m.memory != nil {
		m.memory = memory.New(memoryPolicy(m.activeConfig()), withLoopModePrompt(previousContext, mode))
	}
	m.conversationID = ""
	m.clearResponseReplay()
	if err := m.saveWorkspaceSession(); err != nil {
		m.loopMode, m.messages, m.conversationID = previousMode, previousMessages, previousConversationID
		if previousContext != nil {
			m.memory = memory.New(memoryPolicy(m.activeConfig()), previousContext)
		}
		return err
	}
	return nil
}

func (m *model) runLoopModeCommand(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 1 {
		return "Current mode: " + normalizedLoopMode(m.loopMode) + ". Usage: /mode [default|delegation]"
	}
	if len(fields) != 2 || (fields[1] != loopModeDefault && fields[1] != loopModeDelegation) {
		return "Usage: /mode [default|delegation]"
	}
	if err := m.setLoopMode(fields[1]); err != nil {
		return err.Error()
	}
	return "Mode: " + fields[1]
}
