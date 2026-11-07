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
	"github.com/snowmerak/q/internal/toolbatch"
	"github.com/snowmerak/q/memory"
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
	ctx = client.WithGenerationObserver(ctx, func(attempt client.GenerationAttempt) { emitEvent(ctx, events, Event{generation: &attempt}) })
	contextLimitRecovered := false
	var nextGenerationSettings *client.GenerationSettings
	configuredClient := request.Client
	toolRuntime := request.Tools
	if request.AllowedTools != nil {
		toolRuntime = &allowedToolRuntime{base: toolRuntime, names: request.AllowedTools}
	}
	modelID := request.Model
	reasoningEffort := request.ReasoningEffort
	history := append([]client.Message(nil), request.Messages...)
	conversationID := request.ConversationID
	workingDirectory := request.WorkingDirectory
	auxiliaryDirectories := append([]string(nil), request.AuxiliaryDirectories...)
	activeTask := cloneActiveTask(request.ActiveTask)
	streamEnabled := request.Stream
	coalesceInstructions := request.CoalesceInstructions
	contextPolicy := request.ContextPolicy
	requestExtra := request.Extra
	availableTools := memory.AppendMemoryTools(append(append([]client.Tool(nil), toolRuntime.Tools()...), orchestrationTools()...))
	availableTools = filterTools(availableTools, request.AllowedTools)
	if !catalogHas(availableTools, taskCompleteToolName) {
		activeTask = nil
	}
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
	loopContext.extra = requestExtra
	toolCalls := 0
	taskStarted := activeTask != nil
	var deliveredReply *client.Message
	appendHistory := func(messages ...client.Message) bool {
		if loopContext.manager.ShouldCompactAfterAppend(messages...) {
			if !emitEvent(ctx, events, Event{status: "Compacting context before adding history…"}) {
				return false
			}
			compaction, err := loopContext.CompactBeforeAppend(ctx, configuredClient, modelID, reasoningEffort, messages...)
			if err != nil {
				emitEvent(ctx, events, Event{err: err})
				return false
			}
			if compaction != nil {
				conversationID = ""
				if !emitEvent(ctx, events, Event{compaction: compaction}) {
					return false
				}
			}
		}
		loopContext.Append(messages...)
		return true
	}
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
		requestEstimate := memory.CountMessages(roundHistory)
		request := client.ChatRequest{
			Model: modelID, Messages: providerMessages(agentinstructions.Normalize(roundHistory), coalesceInstructions), ConversationID: conversationID, Tools: availableTools,
			ReasoningEffort: reasoningEffort, WorkingDirectory: workingDirectory, Extra: requestExtra,
		}
		parallel := true
		request.ParallelToolCalls = &parallel
		generationCtx := ctx
		if nextGenerationSettings != nil {
			generationCtx = client.WithGenerationSettings(ctx, *nextGenerationSettings)
			nextGenerationSettings = nil
		}
		var response *client.ChatResponse
		if streamEnabled {
			response, err = streamChatWithEmptyResponseRecovery(generationCtx, configuredClient, request, func(delta chatStreamDelta) bool {
				return emitEvent(ctx, events, Event{streamDelta: &delta})
			})
		} else {
			response, err = chatWithEmptyResponseRecovery(generationCtx, configuredClient, request)
		}
		if err != nil {
			if incomplete, ok := errors.AsType[*client.IncompleteGeneration](err); ok && incomplete.Reason == "context_limit" && !contextLimitRecovered {
				settings, settingsErr := client.ResolveGenerationSettings(generationCtx, configuredClient, modelID)
				if incomplete.Settings != nil {
					settings = *incomplete.Settings
				}
				if settingsErr == nil && settings.SafeReplay && settings.MaxAttempts-(incomplete.Attempts-1) > 0 {
					contextLimitRecovered = true
					compacted, compactErr := loopContext.CompactAfterLimit(ctx, configuredClient, modelID, reasoningEffort)
					if compactErr == nil && compacted != nil && emitEvent(ctx, events, Event{compaction: compacted}) {
						conversationID = ""
						settings.MaxAttempts -= incomplete.Attempts
						nextGenerationSettings = &settings
						continue
					}
					err = errors.Join(err, compactErr)
				}
			}
			emitEvent(ctx, events, Event{err: err})
			return
		}
		if response != nil {
			loopContext.Observe(client.ContextUsage(response), requestEstimate)
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
				if !appendHistory(assistant) {
					return
				}
				// A pre-append compaction starts the next provider conversation.
				response.Choices[0].Message = assistant
				response.ConversationID = conversationID
				emitEvent(ctx, events, Event{
					response: response, complete: true, requestEstimate: requestEstimate, toolCalls: toolCalls,
				})
				return
			}
			reminder := client.Message{
				Role: client.RoleUser,
				Content: "This task was started with task_start and still needs task_complete to record its outcome. " +
					"If the work and user-facing answer are complete, call task_complete now with a brief work summary; do not repeat the answer. " +
					"If more work is required, continue it and deliver the final results before completing the task.",
			}
			if !appendHistory(assistant, reminder) {
				return
			}
			if !emitEvent(ctx, events, Event{message: &assistant, usage: &response.Usage}) {
				return
			}
			if isUserAnswer(assistant) {
				answer := assistant
				deliveredReply = &answer
			}
			if !emitEvent(ctx, events, Event{contextMessage: &reminder}) {
				return
			}
			continue
		}
		// Text accompanying ordinary tool calls is progress, not a final answer.
		// Further work invalidates a previously offered answer for this turn.
		if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Function.Name != taskCompleteToolName {
			deliveredReply = nil
		}
		for index := range assistant.ToolCalls {
			if assistant.ToolCalls[index].ID == "" {
				assistant.ToolCalls[index].ID = fmt.Sprintf("q-call-%d-%d", round+1, index+1)
			}
		}
		instructionLoader := agentinstructions.NewRootSet(workingDirectory, auxiliaryDirectories, loopContext.Messages())
		instructionCalls := make([]client.ToolCall, 0, len(assistant.ToolCalls))
		for _, call := range assistant.ToolCalls {
			if catalogHas(availableTools, call.Function.Name) {
				instructionCalls = append(instructionCalls, call)
			}
		}
		newInstructions := instructionLoader.ForToolCalls(instructionCalls)
		if len(newInstructions) > 0 {
			if !appendHistory(newInstructions...) {
				return
			}
			for index := range newInstructions {
				message := newInstructions[index]
				if !emitEvent(ctx, events, Event{message: &message}) {
					return
				}
			}
			if !appendHistory(assistant) {
				return
			}
			if !emitEvent(ctx, events, Event{message: &assistant, usage: &response.Usage}) {
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
				if !appendHistory(message) {
					return
				}
				if !emitEvent(ctx, events, Event{message: &message, toolIsError: true}) {
					return
				}
			}
			continue
		}
		if !appendHistory(assistant) {
			return
		}
		if !emitEvent(ctx, events, Event{message: &assistant, usage: &response.Usage}) {
			return
		}
		batch := toolbatch.New(assistant.ToolCalls, toolbatch.IsLoopTool, func(ctx context.Context, _ int, call client.ToolCall) (client.ToolResult, error) {
			if !catalogHas(availableTools, call.Function.Name) {
				return client.ToolResult{}, fmt.Errorf("tool %q is not allowed in this session", call.Function.Name)
			}
			return toolRuntime.Call(ctx, call)
		})
		batch.Before = func(_ int, call client.ToolCall) error {
			if !catalogHas(availableTools, call.Function.Name) {
				return nil
			}
			if !emitEvent(ctx, events, Event{call: &call}) {
				return ctx.Err()
			}
			return nil
		}
		for index, call := range assistant.ToolCalls {
			if ctx.Err() != nil {
				return
			}
			callCopy := call
			if !catalogHas(availableTools, call.Function.Name) {
				message := orchestrationToolResult(call, fmt.Sprintf("tool %q is not allowed in this session", call.Function.Name), true)
				if !appendHistory(message) || !emitEvent(ctx, events, Event{message: &message, toolIsError: true}) {
					return
				}
				continue
			}
			if toolbatch.IsLoopTool(call) && !emitEvent(ctx, events, Event{call: &callCopy}) {
				return
			}
			if call.Function.Name == taskStartToolName {
				input, parseErr := parseTaskStart(call.Function.Arguments)
				if parseErr == nil && taskStarted {
					parseErr = errors.New("another task_start lifecycle is already active")
				}
				if parseErr != nil {
					message := orchestrationToolResult(call, "invalid task_start arguments: "+parseErr.Error(), true)
					if !appendHistory(message) {
						return
					}
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
				if !appendHistory(message) {
					return
				}
				if !emitEvent(ctx, events, Event{message: &message}) {
					return
				}
				started := &workspace.ActiveTask{
					Objective: input.Objective, CompletionCriteria: append([]string(nil), input.CompletionCriteria...),
					StartedAt: time.Now().UTC(),
				}
				if !emitEvent(ctx, events, Event{taskStarted: started}) {
					return
				}
				continue
			}
			if call.Function.Name == askToUserToolName {
				input, parseErr := parseAskToUser(call.Function.Arguments)
				if parseErr != nil {
					message := orchestrationToolResult(call, "invalid ask_to_user arguments: "+parseErr.Error(), true)
					if !appendHistory(message) {
						return
					}
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
					if errors.Is(answer.Err, errInteractionUnavailable) {
						message := orchestrationToolResult(
							call,
							"interaction_unavailable: interactive input is unavailable in this host; continue with the available information or finish the task as blocked",
							true,
						)
						if !appendHistory(message) {
							return
						}
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
				if !appendHistory(message) {
					return
				}
				if !emitEvent(ctx, events, Event{message: &message}) {
					return
				}
				continue
			}
			if call.Function.Name == taskCompleteToolName {
				if !taskStarted {
					message := orchestrationToolResult(call, "task_complete requires an active task_start lifecycle", true)
					if !appendHistory(message) {
						return
					}
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
					if !appendHistory(message) {
						return
					}
					if !emitEvent(ctx, events, Event{message: &message, toolIsError: true}) {
						return
					}
					continue
				}
				if isUserAnswer(assistant) {
					answer := assistant
					deliveredReply = &answer
				}
				body, _ := json.Marshal(completion)
				message := orchestrationToolResult(call, string(body), false)
				if !appendHistory(message) {
					return
				}
				if !emitEvent(ctx, events, Event{message: &message}) {
					return
				}
				if !emitEvent(ctx, events, Event{taskCompleted: true}) {
					return
				}
				if !emitEvent(ctx, events, Event{status: "Finalizing task…"}) {
					return
				}
				request.Messages = loopContext.Messages()
				request.ConversationID = conversationID
				finishOptions := client.FinishToolTurnOptions{}
				if deliveredReply == nil {
					finishOptions.Instruction = terminalAnswerInstruction
				}
				finished, finishErr := client.FinishToolTurn(ctx, request, func(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
					// Rejected terminal calls can trigger pre-append compaction too.
					// Keep the provider request on the loop's current context/thread.
					history := loopContext.Messages()
					last := len(history) - 1
					previous := request.Messages[len(request.Messages)-1]
					if last >= 0 && history[last].ToolCallID == previous.ToolCallID && previous.Role == client.RoleTool {
						history[last].Content = previous.Content
					}
					request.Messages = history
					request.ConversationID = conversationID
					requestEstimate = memory.CountMessages(request.Messages)
					request.Messages = providerMessages(agentinstructions.Normalize(request.Messages), coalesceInstructions)
					var response *client.ChatResponse
					var err error
					if streamEnabled {
						response, err = streamChatWithConversationRecovery(ctx, configuredClient, request, func(delta chatStreamDelta) bool {
							if deliveredReply != nil {
								return true
							}
							return emitEvent(ctx, events, Event{streamDelta: &delta})
						})
					} else {
						response, err = chatWithConversationRecovery(ctx, configuredClient, request)
					}
					if response != nil && response.ConversationID != "" {
						conversationID = response.ConversationID
					}
					return response, err
				}, func(message client.Message) error {
					// The final provider reply is handled once below. Preserve extra
					// calls and their rejection results without executing them.
					if message.Role == client.RoleAssistant && len(message.ToolCalls) == 0 {
						return nil
					}
					if !appendHistory(message) {
						return errors.New("agent loop: could not reserve context for terminal history")
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
				}, finishOptions)
				if finishErr != nil {
					emitEvent(ctx, events, Event{err: finishErr})
					return
				}
				response = finished.Response
				providerReply := response.Choices[0].Message
				if providerReply.Role == "" {
					providerReply.Role = client.RoleAssistant
				}
				loopContext.Observe(client.ContextUsage(response), requestEstimate)
				if !appendHistory(providerReply) {
					return
				}
				var responseContext *client.Message
				if deliveredReply != nil || strings.TrimSpace(providerReply.TextContent()) == "" {
					// Retain the real provider reply and replay metadata in context,
					// independently of the user-facing result projection.
					responseContext = &providerReply
					projected := *response
					projected.Choices = append([]client.Choice(nil), response.Choices...)
					visible := client.Message{Role: client.RoleAssistant, Name: "q_task_complete_reply", Content: renderTaskCompletion(completion)}
					if deliveredReply != nil {
						visible.Content = deliveredReply.TextContent()
					}
					projected.Choices[0].Message = visible
					response = &projected
				}
				response.ConversationID = conversationID
				emitEvent(ctx, events, Event{
					response: response, responseContext: responseContext, replyAlreadyEmitted: deliveredReply != nil, complete: true, outcome: completion.Outcome, requestEstimate: requestEstimate, toolCalls: toolCalls,
				})
				return
			}
			result, handled := loopContext.CallMemoryTool(call)
			var callErr error
			if !handled {
				result, callErr = batch.Call(ctx, index)
			}
			if ctx.Err() != nil {
				return
			}
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
			if !appendHistory(message) {
				return
			}
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
