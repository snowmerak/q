package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/snowmerak/q/agentinstructions"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/thinker"
	"github.com/snowmerak/q/workspace"
)

// RunAgentLoop runs Q's existing model/tool loop. It is synchronous; callers
// commonly invoke it in a goroutine while consuming events. The function owns
// and closes events, but does not close the injected client or tool runtime.
func RunAgentLoop(ctx context.Context, request Request, events chan<- Event) {
	if events == nil {
		return
	}
	defer close(events)
	if request.Client == nil {
		emitEvent(ctx, events, Event{err: errors.New("agent loop client is required")})
		return
	}
	if request.Tools == nil {
		emitEvent(ctx, events, Event{err: errors.New("agent loop tool runtime is required")})
		return
	}
	configuredClient := request.Client
	toolRuntime := request.Tools
	modelID := request.Model
	reasoningEffort := request.ReasoningEffort
	history := append([]client.Message(nil), request.Messages...)
	conversationID := request.ConversationID
	workingDirectory := request.WorkingDirectory
	activeTask := cloneActiveTask(request.ActiveTask)
	streamEnabled := request.Stream
	coalesceInstructions := request.CoalesceInstructions
	contextPolicy := request.ContextPolicy
	availableTools := append(append([]client.Tool(nil), toolRuntime.Tools()...), orchestrationTools()...)
	hintedSkillIDs := knownSkillIDs(history)
	if len(history) > 0 && history[len(history)-1].Role == client.RoleUser {
		index := len(history) - 1
		original := history[index]
		updated := original
		if activeTask != nil {
			updated = appendActiveTaskContext(updated, *activeTask)
		}
		if !strings.Contains(updated.TextContent(), skillHintsTag) {
			query := original.TextContent()
			if activeTask != nil {
				query = skillHintQueryForActiveTask(*activeTask, query)
			}
			hints := automaticSkillHints(ctx, toolRuntime, query, "user_input", hintedSkillIDs)
			updated = appendSkillHintContext(updated, hints)
		}
		if updated.TextContent() != original.TextContent() {
			history[index] = updated
			replacement := &ContextReplacement{Index: index, Message: updated}
			if !emitEvent(ctx, events, Event{contextReplace: replacement}) {
				return
			}
		}
	}
	loopContext := newAgentLoopContext(contextPolicy, history, availableTools)
	toolCalls := 0
	taskStarted := activeTask != nil
	for round := 0; ; round++ {
		if loopContext.ShouldCompact() {
			if !emitEvent(ctx, events, Event{status: "Compacting context…"}) {
				return
			}
		}
		compaction, err := loopContext.CompactIfNeeded(ctx, configuredClient, modelID, reasoningEffort)
		if err != nil {
			emitEvent(ctx, events, Event{err: err})
			return
		}
		if compaction != nil {
			conversationID = ""
			if !emitEvent(ctx, events, Event{compaction: compaction}) {
				return
			}
		}
		roundHistory := loopContext.Messages()
		appendHistory := func(messages ...client.Message) {
			loopContext.Append(messages...)
			roundHistory = append(roundHistory, messages...)
		}
		requestEstimate := memory.CountMessages(roundHistory)
		request := client.ChatRequest{
			Model: modelID, Messages: providerMessages(agentinstructions.Normalize(roundHistory), coalesceInstructions), ConversationID: conversationID, Tools: availableTools,
			ReasoningEffort: reasoningEffort, WorkingDirectory: workingDirectory,
		}
		var response *client.ChatResponse
		err = nil
		if streamEnabled {
			response, err = streamChatWithEmptyResponseRecovery(ctx, configuredClient, request, func(delta chatStreamDelta) bool {
				return emitEvent(ctx, events, Event{streamDelta: &delta})
			})
		} else {
			response, err = chatWithEmptyResponseRecovery(ctx, configuredClient, request)
		}
		if err != nil {
			emitEvent(ctx, events, Event{err: err})
			return
		}
		if response != nil {
			loopContext.Observe(response.Usage, requestEstimate)
		}
		if response == nil || len(response.Choices) == 0 {
			emitEvent(ctx, events, Event{response: response, complete: true})
			return
		}
		assistant := response.Choices[0].Message
		if assistant.Role == "" {
			assistant.Role = client.RoleAssistant
		}
		if response.ConversationID != "" {
			conversationID = response.ConversationID
		}
		if len(assistant.ToolCalls) == 0 {
			if !taskStarted {
				response.Choices[0].Message = assistant
				response.ConversationID = conversationID
				emitEvent(ctx, events, Event{
					response: response, complete: true, requestEstimate: requestEstimate, toolCalls: toolCalls,
				})
				return
			}
			appendHistory(assistant, client.Message{
				Role: client.RoleUser,
				Content: "This task was started with task_start and is not complete until you call task_complete. " +
					"Call task_complete now with the final outcome and summary; " +
					"if more work is required, continue the work first.",
			})
			continue
		}
		for index := range assistant.ToolCalls {
			if assistant.ToolCalls[index].ID == "" {
				assistant.ToolCalls[index].ID = fmt.Sprintf("q-call-%d-%d", round+1, index+1)
			}
		}
		instructionLoader := agentinstructions.New(workingDirectory, loopContext.Messages())
		newInstructions := instructionLoader.ForToolCalls(assistant.ToolCalls)
		if len(newInstructions) > 0 {
			appendHistory(newInstructions...)
			for index := range newInstructions {
				message := newInstructions[index]
				if !emitEvent(ctx, events, Event{message: &message}) {
					return
				}
			}
			appendHistory(assistant)
			if !emitEvent(ctx, events, Event{message: &assistant}) {
				return
			}
			sources := strings.Join(agentinstructions.Sources(newInstructions), ", ")
			for _, call := range assistant.ToolCalls {
				callCopy := call
				if !emitEvent(ctx, events, Event{call: &callCopy}) {
					return
				}
				message := orchestrationToolResult(
					call,
					"workspace instructions were loaded from "+sources+"; review them and retry any still-appropriate tool call",
					true,
				)
				appendHistory(message)
				if !emitEvent(ctx, events, Event{message: &message, toolIsError: true}) {
					return
				}
			}
			continue
		}
		appendHistory(assistant)
		if !emitEvent(ctx, events, Event{message: &assistant}) {
			return
		}
		for _, call := range assistant.ToolCalls {
			callCopy := call
			if !emitEvent(ctx, events, Event{call: &callCopy}) {
				return
			}
			if call.Function.Name == taskStartToolName {
				input, parseErr := parseTaskStart(call.Function.Arguments)
				if parseErr == nil && taskStarted {
					parseErr = errors.New("another task_start lifecycle is already active")
				}
				if parseErr != nil {
					message := orchestrationToolResult(call, "invalid task_start arguments: "+parseErr.Error(), true)
					appendHistory(message)
					if !emitEvent(ctx, events, Event{message: &message, toolIsError: true}) {
						return
					}
					continue
				}
				taskStarted = true
				hints := automaticSkillHints(ctx, toolRuntime, skillHintQueryForTask(input), "task_start", hintedSkillIDs)
				body, _ := json.Marshal(taskStartOutput{
					Started: true, Objective: input.Objective,
					CompletionCriteria: append([]string(nil), input.CompletionCriteria...), SkillHints: hints,
				})
				message := orchestrationToolResult(call, string(body), false)
				appendHistory(message)
				if !emitEvent(ctx, events, Event{message: &message}) {
					return
				}
				started := &workspace.ActiveTask{
					Objective: input.Objective, CompletionCriteria: append([]string(nil), input.CompletionCriteria...),
					StartedAt: time.Now().UTC(),
				}
				activeTask = started
				if !emitEvent(ctx, events, Event{taskStarted: started}) {
					return
				}
				continue
			}
			if call.Function.Name == askToUserToolName {
				input, parseErr := parseAskToUser(call.Function.Arguments)
				if parseErr != nil {
					message := orchestrationToolResult(call, "invalid ask_to_user arguments: "+parseErr.Error(), true)
					appendHistory(message)
					if !emitEvent(ctx, events, Event{message: &message, toolIsError: true}) {
						return
					}
					continue
				}
				answerChannel := make(chan askToUserOutput, 1)
				if !emitEvent(ctx, events, Event{question: &input, answer: answerChannel}) {
					return
				}
				var answer askToUserOutput
				select {
				case answer = <-answerChannel:
				case <-ctx.Done():
					return
				}
				if answer.Err != nil {
					if errors.Is(answer.Err, errRemoteInteractionUnavailable) {
						message := orchestrationToolResult(
							call,
							"interaction_unavailable: interactive input is unavailable in q remote mode; continue with the available information or finish the task as blocked",
							true,
						)
						appendHistory(message)
						if !emitEvent(ctx, events, Event{message: &message, toolIsError: true}) {
							return
						}
						continue
					}
					emitEvent(ctx, events, Event{err: answer.Err})
					return
				}
				answer.SkillHints = automaticSkillHints(
					ctx, toolRuntime, skillHintQueryForAnswer(input, answer), "user_answer", hintedSkillIDs,
				)
				body, _ := json.Marshal(answer)
				message := orchestrationToolResult(call, string(body), false)
				appendHistory(message)
				if !emitEvent(ctx, events, Event{message: &message}) {
					return
				}
				continue
			}
			if call.Function.Name == taskCompleteToolName {
				if !taskStarted {
					message := orchestrationToolResult(call, "task_complete requires an active task_start lifecycle", true)
					appendHistory(message)
					if !emitEvent(ctx, events, Event{message: &message, toolIsError: true}) {
						return
					}
					continue
				}
				completion, parseErr := parseTaskComplete(call.Function.Arguments)
				if parseErr == nil && len(assistant.ToolCalls) != 1 {
					parseErr = errors.New("task_complete must be the only tool call in its turn")
				}
				if parseErr != nil {
					message := orchestrationToolResult(call, "invalid task_complete arguments: "+parseErr.Error(), true)
					appendHistory(message)
					if !emitEvent(ctx, events, Event{message: &message, toolIsError: true}) {
						return
					}
					continue
				}
				body, _ := json.Marshal(completion)
				message := orchestrationToolResult(call, string(body), false)
				appendHistory(message)
				if !emitEvent(ctx, events, Event{message: &message}) {
					return
				}
				if !emitEvent(ctx, events, Event{taskCompleted: true}) {
					return
				}
				if !emitEvent(ctx, events, Event{
					learningName: thinker.TaskCompleteEventName, learningPayload: append(json.RawMessage(nil), body...),
				}) {
					return
				}
				if !emitEvent(ctx, events, Event{status: "Finalizing task…"}) {
					return
				}
				request.Messages = roundHistory
				request.ConversationID = conversationID
				finished, finishErr := client.FinishToolTurn(ctx, request, func(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
					requestEstimate = memory.CountMessages(request.Messages)
					request.Messages = providerMessages(agentinstructions.Normalize(request.Messages), coalesceInstructions)
					if streamEnabled {
						return streamChatWithConversationRecovery(ctx, configuredClient, request, func(chatStreamDelta) bool { return true })
					}
					return chatWithConversationRecovery(ctx, configuredClient, request)
				}, func(message client.Message) error {
					// Keep the structured completion as the one user-visible final
					// answer, but archive any extra calls and their rejection results.
					if message.Role == client.RoleAssistant && len(message.ToolCalls) == 0 {
						return nil
					}
					if !emitEvent(ctx, events, Event{message: &message, toolIsError: message.Role == client.RoleTool}) {
						return ctx.Err()
					}
					for _, extra := range message.ToolCalls {
						if !emitEvent(ctx, events, Event{call: &extra}) {
							return ctx.Err()
						}
					}
					return nil
				})
				if finishErr != nil {
					emitEvent(ctx, events, Event{err: finishErr})
					return
				}
				response = finished.Response
				response.Choices[0].Message = client.Message{
					Role: client.RoleAssistant, Name: thinker.TaskCompletionReplyName,
					Content: renderTaskCompletion(completion),
				}
				if response.ConversationID == "" {
					response.ConversationID = conversationID
				}
				emitEvent(ctx, events, Event{
					response: response, complete: true, outcome: completion.Outcome, requestEstimate: requestEstimate, toolCalls: toolCalls,
				})
				return
			}
			result, callErr := toolRuntime.Call(ctx, call)
			if callErr != nil {
				result = client.ToolResult{Content: callErr.Error(), IsError: true}
			}
			content := result.Content
			if result.IsError {
				content = "Tool error: " + content
			}
			message := client.Message{
				Role: client.RoleTool, Name: call.Function.Name,
				ToolCallID: call.ID, Content: content,
			}
			appendHistory(message)
			toolCalls++
			if !emitEvent(ctx, events, Event{message: &message, toolIsError: result.IsError}) {
				return
			}
		}
	}
}

func orchestrationToolResult(call client.ToolCall, content string, isError bool) client.Message {
	if isError {
		content = "Tool error: " + content
	}
	return client.Message{
		Role: client.RoleTool, Name: call.Function.Name,
		ToolCallID: call.ID, Content: content,
	}
}

func emitEvent(ctx context.Context, events chan<- Event, event Event) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func cloneActiveTask(task *workspace.ActiveTask) *workspace.ActiveTask {
	if task == nil {
		return nil
	}
	cloned := *task
	cloned.CompletionCriteria = append([]string(nil), task.CompletionCriteria...)
	return &cloned
}
