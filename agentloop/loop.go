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
)

// TaskCompletionReplyName is assigned to the synthetic assistant reply created
// from a successful task_complete result. Learning projections use this stable
// name to recognize the task boundary.
const TaskCompletionReplyName = "q_task_complete_reply"

// Run executes one complete model/tool turn. It does not create or close the
// injected model client and tool runtime, and it does not persist Result or
// Event values. Cancellation is controlled exclusively by ctx.
func Run(ctx context.Context, request Request, hooks Hooks) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("agentloop: context is required")
	}
	if request.Client == nil {
		return Result{}, errors.New("agentloop: model client is required")
	}
	if request.Tools == nil {
		return Result{}, errors.New("agentloop: tool runtime is required")
	}
	if request.MaxRounds < 0 {
		return Result{}, errors.New("agentloop: max rounds must not be negative")
	}

	usageRole := strings.TrimSpace(request.UsageRole)
	if usageRole == "" {
		usageRole = "main"
	}
	ctx = client.WithUsageRole(ctx, usageRole)

	runtimeTools := request.Tools.Tools()
	if err := validateRuntimeTools(runtimeTools); err != nil {
		return Result{}, err
	}
	availableTools := append(append([]client.Tool(nil), runtimeTools...), OrchestrationTools()...)
	history := append([]client.Message(nil), request.Messages...)
	hintedSkillIDs := knownSkillIDs(history)
	activeTask := cloneTask(request.ActiveTask)

	if len(history) > 0 && history[len(history)-1].Role == client.RoleUser {
		index := len(history) - 1
		original := history[index]
		updated := original
		if activeTask != nil {
			updated = appendActiveTaskContext(updated, *activeTask)
		}
		if !strings.Contains(updated.TextContent(), SkillHintsTag) {
			query := original.TextContent()
			if activeTask != nil {
				query = skillHintQueryForActiveTask(*activeTask, query)
			}
			hints := automaticSkillHints(ctx, runtimeTools, request.SkillHints, query, "user_input", hintedSkillIDs)
			updated = appendSkillHintContext(updated, hints)
		}
		if updated.TextContent() != original.TextContent() {
			history[index] = updated
			if err := emit(hooks, Event{
				Kind:        EventContextReplace,
				Replacement: ContextReplacement{Index: index, Message: updated},
			}); err != nil {
				return Result{}, err
			}
		}
	}

	loopContext := newLoopContext(request.ContextPolicy, history, availableTools)
	conversationID := request.ConversationID
	toolCalls := 0
	taskStarted := activeTask != nil
	requestEstimate := 0

	for round := 0; ; round++ {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if request.MaxRounds > 0 && round >= request.MaxRounds {
			return Result{}, fmt.Errorf("%w: %d", ErrRoundLimit, request.MaxRounds)
		}
		if loopContext.shouldCompact() {
			if err := emit(hooks, Event{Kind: EventStatus, Status: "Compacting context…"}); err != nil {
				return Result{}, err
			}
		}
		compaction, err := loopContext.compactIfNeeded(ctx, request.Client, request.Model, request.ReasoningEffort)
		if err != nil {
			return Result{}, err
		}
		if compaction != nil {
			conversationID = ""
			if err := emit(hooks, Event{Kind: EventCompaction, Compaction: *compaction}); err != nil {
				return Result{}, err
			}
		}

		roundHistory := loopContext.messages()
		appendHistory := func(messages ...client.Message) {
			loopContext.append(messages...)
			roundHistory = append(roundHistory, messages...)
		}
		requestEstimate = memory.CountMessages(roundHistory)
		modelRequest := client.ChatRequest{
			Model: request.Model, Messages: providerMessages(agentinstructions.Normalize(roundHistory), request.CoalesceInstructions),
			ConversationID: conversationID, Tools: availableTools,
			ReasoningEffort: request.ReasoningEffort, WorkingDirectory: request.WorkingDirectory,
		}
		var response *client.ChatResponse
		if request.Stream {
			response, err = streamChatWithEmptyResponseRecovery(ctx, request.Client, modelRequest, func(delta StreamDelta) error {
				return emit(hooks, Event{Kind: EventStreamDelta, Stream: delta})
			})
		} else {
			response, err = chatWithEmptyResponseRecovery(ctx, request.Client, modelRequest)
		}
		if err != nil {
			return Result{}, err
		}
		if response != nil {
			loopContext.observe(response.Usage, requestEstimate)
		}
		if response == nil || len(response.Choices) == 0 {
			return terminalResult(response, loopContext, conversationID, "", requestEstimate, toolCalls), nil
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
				loopContext.append(assistant)
				return terminalResult(response, loopContext, conversationID, "", requestEstimate, toolCalls), nil
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
		instructionLoader := agentinstructions.New(request.WorkingDirectory, loopContext.messages())
		newInstructions := instructionLoader.ForToolCalls(assistant.ToolCalls)
		if len(newInstructions) > 0 {
			appendHistory(newInstructions...)
			for _, instruction := range newInstructions {
				if err := emit(hooks, Event{Kind: EventMessage, Message: instruction}); err != nil {
					return Result{}, err
				}
			}
			appendHistory(assistant)
			if err := emit(hooks, Event{Kind: EventMessage, Message: assistant}); err != nil {
				return Result{}, err
			}
			sources := strings.Join(agentinstructions.Sources(newInstructions), ", ")
			for _, call := range assistant.ToolCalls {
				if err := emit(hooks, Event{Kind: EventToolCall, ToolCall: call}); err != nil {
					return Result{}, err
				}
				message := orchestrationToolResult(
					call,
					"workspace instructions were loaded from "+sources+"; review them and retry any still-appropriate tool call",
					true,
				)
				appendHistory(message)
				if err := emit(hooks, Event{Kind: EventMessage, Message: message, ToolIsError: true}); err != nil {
					return Result{}, err
				}
			}
			continue
		}

		appendHistory(assistant)
		if err := emit(hooks, Event{Kind: EventMessage, Message: assistant}); err != nil {
			return Result{}, err
		}
		for _, call := range assistant.ToolCalls {
			if err := emit(hooks, Event{Kind: EventToolCall, ToolCall: call}); err != nil {
				return Result{}, err
			}
			switch call.Function.Name {
			case TaskStartToolName:
				input, parseErr := ParseTaskStart(call.Function.Arguments)
				if parseErr == nil && taskStarted {
					parseErr = errors.New("another task_start lifecycle is already active")
				}
				if parseErr != nil {
					message := orchestrationToolResult(call, "invalid task_start arguments: "+parseErr.Error(), true)
					appendHistory(message)
					if err := emit(hooks, Event{Kind: EventMessage, Message: message, ToolIsError: true}); err != nil {
						return Result{}, err
					}
					continue
				}
				taskStarted = true
				hints := automaticSkillHints(
					ctx, runtimeTools, request.SkillHints, skillHintQueryForTask(input), "task_start", hintedSkillIDs,
				)
				body, _ := json.Marshal(taskStartOutput{
					Started: true, Objective: input.Objective,
					CompletionCriteria: append([]string(nil), input.CompletionCriteria...), SkillHints: hints,
				})
				message := orchestrationToolResult(call, string(body), false)
				appendHistory(message)
				if err := emit(hooks, Event{Kind: EventMessage, Message: message}); err != nil {
					return Result{}, err
				}
				activeTask = &Task{
					Objective: input.Objective, CompletionCriteria: append([]string(nil), input.CompletionCriteria...),
					StartedAt: time.Now().UTC(),
				}
				if err := emit(hooks, Event{Kind: EventTaskStarted, Task: *activeTask}); err != nil {
					return Result{}, err
				}

			case AskToUserToolName:
				question, parseErr := ParseQuestion(call.Function.Arguments)
				if parseErr != nil {
					message := orchestrationToolResult(call, "invalid ask_to_user arguments: "+parseErr.Error(), true)
					appendHistory(message)
					if err := emit(hooks, Event{Kind: EventMessage, Message: message, ToolIsError: true}); err != nil {
						return Result{}, err
					}
					continue
				}
				var answer Answer
				var answerErr error
				if hooks.Ask == nil {
					answerErr = ErrInteractionUnavailable
				} else {
					answer, answerErr = hooks.Ask(ctx, question)
				}
				if answerErr != nil {
					if errors.Is(answerErr, ErrInteractionUnavailable) {
						message := orchestrationToolResult(
							call,
							"interaction_unavailable: interactive input is unavailable; continue with the available information or finish the task as blocked",
							true,
						)
						appendHistory(message)
						if err := emit(hooks, Event{Kind: EventMessage, Message: message, ToolIsError: true}); err != nil {
							return Result{}, err
						}
						continue
					}
					return Result{}, answerErr
				}
				hints := automaticSkillHints(
					ctx, runtimeTools, request.SkillHints, skillHintQueryForAnswer(question, answer), "user_answer", hintedSkillIDs,
				)
				body, _ := json.Marshal(answerOutput{Answer: answer, SkillHints: hints})
				message := orchestrationToolResult(call, string(body), false)
				appendHistory(message)
				if err := emit(hooks, Event{Kind: EventMessage, Message: message}); err != nil {
					return Result{}, err
				}

			case TaskCompleteToolName:
				if !taskStarted {
					message := orchestrationToolResult(call, "task_complete requires an active task_start lifecycle", true)
					appendHistory(message)
					if err := emit(hooks, Event{Kind: EventMessage, Message: message, ToolIsError: true}); err != nil {
						return Result{}, err
					}
					continue
				}
				completion, parseErr := ParseCompletion(call.Function.Arguments)
				if parseErr == nil && len(assistant.ToolCalls) != 1 {
					parseErr = errors.New("task_complete must be the only tool call in its turn")
				}
				if parseErr != nil {
					message := orchestrationToolResult(call, "invalid task_complete arguments: "+parseErr.Error(), true)
					appendHistory(message)
					if err := emit(hooks, Event{Kind: EventMessage, Message: message, ToolIsError: true}); err != nil {
						return Result{}, err
					}
					continue
				}
				body, _ := json.Marshal(completion)
				message := orchestrationToolResult(call, string(body), false)
				appendHistory(message)
				if err := emit(hooks, Event{Kind: EventMessage, Message: message}); err != nil {
					return Result{}, err
				}
				if err := emit(hooks, Event{Kind: EventTaskCompleted, Completion: completion}); err != nil {
					return Result{}, err
				}
				if err := emit(hooks, Event{Kind: EventStatus, Status: "Finalizing task…"}); err != nil {
					return Result{}, err
				}

				modelRequest.Messages = roundHistory
				modelRequest.ConversationID = conversationID
				finished, finishErr := client.FinishToolTurn(ctx, modelRequest, func(ctx context.Context, next client.ChatRequest) (*client.ChatResponse, error) {
					requestEstimate = memory.CountMessages(next.Messages)
					next.Messages = providerMessages(agentinstructions.Normalize(next.Messages), request.CoalesceInstructions)
					if request.Stream {
						return streamChatWithConversationRecovery(ctx, request.Client, next, func(StreamDelta) error { return nil })
					}
					return chatWithConversationRecovery(ctx, request.Client, next)
				}, func(message client.Message) error {
					if message.Role == client.RoleAssistant && len(message.ToolCalls) == 0 {
						return nil
					}
					if err := emit(hooks, Event{
						Kind: EventMessage, Message: message, ToolIsError: message.Role == client.RoleTool,
					}); err != nil {
						return err
					}
					for _, extra := range message.ToolCalls {
						if err := emit(hooks, Event{Kind: EventToolCall, ToolCall: extra}); err != nil {
							return err
						}
					}
					return nil
				})
				if finishErr != nil {
					return Result{}, finishErr
				}
				response = finished.Response
				finalAssistant := client.Message{
					Role: client.RoleAssistant, Name: TaskCompletionReplyName,
					Content: RenderCompletion(completion),
				}
				response.Choices[0].Message = finalAssistant
				if response.ConversationID == "" {
					response.ConversationID = conversationID
				}
				conversationID = response.ConversationID
				loopContext.append(finalAssistant)
				return terminalResult(
					response, loopContext, conversationID, completion.Outcome, requestEstimate, toolCalls,
				), nil

			default:
				toolResult, callErr := request.Tools.Call(ctx, call)
				if callErr != nil {
					toolResult = client.ToolResult{Content: callErr.Error(), IsError: true}
				}
				message := client.ToolResultMessage(call, toolResult)
				appendHistory(message)
				toolCalls++
				if err := emit(hooks, Event{Kind: EventMessage, Message: message, ToolIsError: toolResult.IsError}); err != nil {
					return Result{}, err
				}
			}
		}
	}
}

func validateRuntimeTools(tools []client.Tool) error {
	reserved := map[string]bool{
		TaskStartToolName: true, AskToUserToolName: true, TaskCompleteToolName: true,
	}
	for _, tool := range tools {
		if reserved[tool.Function.Name] {
			return fmt.Errorf("agentloop: tool runtime uses reserved tool name %q", tool.Function.Name)
		}
	}
	return nil
}

func cloneTask(task *Task) *Task {
	if task == nil {
		return nil
	}
	cloned := *task
	cloned.CompletionCriteria = append([]string(nil), task.CompletionCriteria...)
	return &cloned
}

func emit(hooks Hooks, event Event) error {
	if hooks.Event == nil {
		return nil
	}
	if err := hooks.Event(event); err != nil {
		return fmt.Errorf("agentloop: deliver %s event: %w", event.Kind, err)
	}
	return nil
}

func terminalResult(
	response *client.ChatResponse,
	loopContext *loopContext,
	conversationID string,
	outcome string,
	requestEstimate int,
	toolCalls int,
) Result {
	return Result{
		Response: response, Context: loopContext.messages(), ConversationID: conversationID,
		Outcome: outcome, RequestEstimate: requestEstimate, ToolCalls: toolCalls,
	}
}
