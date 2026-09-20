package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
	"github.com/snowmerak/q/thinker"
	"github.com/snowmerak/q/workspace"
)

// streamAgentLoop keeps the existing app event contract while delegating the
// model/tool state machine to the public, embeddable agentloop package.
func streamAgentLoop(
	ctx context.Context,
	configuredClient chatClient,
	toolRuntime agentToolRuntime,
	modelID, reasoningEffort string,
	history []client.Message,
	conversationID string,
	workingDirectory string,
	activeTask *workspace.ActiveTask,
	streamEnabled bool,
	coalesceInstructions bool,
	contextPolicy memory.Policy,
	events chan<- agentEvent,
) {
	defer close(events)

	result, err := agentloop.Run(ctx, agentloop.Request{
		Client:               configuredClient,
		Tools:                toolRuntime,
		SkillHints:           appSkillHintSearcher(toolRuntime),
		Messages:             history,
		Model:                modelID,
		ReasoningEffort:      reasoningEffort,
		ConversationID:       conversationID,
		WorkingDirectory:     workingDirectory,
		ActiveTask:           publicActiveTask(activeTask),
		ContextPolicy:        contextPolicy,
		Stream:               streamEnabled,
		CoalesceInstructions: coalesceInstructions,
	}, agentloop.Hooks{
		Event: func(event agentloop.Event) error {
			return bridgeAgentEvent(ctx, events, event)
		},
		Ask: func(ctx context.Context, question agentloop.Question) (agentloop.Answer, error) {
			answerChannel := make(chan askToUserOutput, 1)
			if !emitAgentEvent(ctx, events, agentEvent{question: &question, answer: answerChannel}) {
				return agentloop.Answer{}, ctx.Err()
			}
			select {
			case answer := <-answerChannel:
				if errors.Is(answer.Err, errRemoteInteractionUnavailable) {
					return agentloop.Answer{}, agentloop.ErrInteractionUnavailable
				}
				return agentloop.Answer{
					SelectedChoiceID: answer.SelectedChoiceID,
					Freeform:         answer.Freeform,
				}, answer.Err
			case <-ctx.Done():
				return agentloop.Answer{}, ctx.Err()
			}
		},
	})
	if err != nil {
		emitAgentEvent(ctx, events, agentEvent{err: err})
		return
	}
	emitAgentEvent(ctx, events, agentEvent{
		response: result.Response, outcome: result.Outcome,
		requestEstimate: result.RequestEstimate, toolCalls: result.ToolCalls,
	})
}

func publicActiveTask(task *workspace.ActiveTask) *agentloop.Task {
	if task == nil {
		return nil
	}
	return &agentloop.Task{
		Objective: task.Objective, CompletionCriteria: append([]string(nil), task.CompletionCriteria...),
		StartedAt: task.StartedAt,
	}
}

func bridgeAgentEvent(ctx context.Context, events chan<- agentEvent, event agentloop.Event) error {
	var bridged agentEvent
	switch event.Kind {
	case agentloop.EventStatus:
		bridged.status = event.Status
	case agentloop.EventMessage:
		message := event.Message
		bridged.message = &message
		bridged.toolIsError = event.ToolIsError
	case agentloop.EventToolCall:
		call := event.ToolCall
		bridged.call = &call
	case agentloop.EventStreamDelta:
		bridged.streamDelta = &chatStreamDelta{
			Kind: string(event.Stream.Kind), Content: event.Stream.Content, Start: event.Stream.Start,
		}
	case agentloop.EventContextReplace:
		bridged.contextReplace = &agentContextReplacement{
			Index: event.Replacement.Index, Message: event.Replacement.Message,
		}
	case agentloop.EventCompaction:
		bridged.compaction = &agentContextCompaction{
			Plan: event.Compaction.Plan, Summary: event.Compaction.Summary,
		}
	case agentloop.EventTaskStarted:
		bridged.taskStarted = &workspace.ActiveTask{
			Objective:          event.Task.Objective,
			CompletionCriteria: append([]string(nil), event.Task.CompletionCriteria...),
			StartedAt:          event.Task.StartedAt,
		}
	case agentloop.EventTaskCompleted:
		if !emitAgentEvent(ctx, events, agentEvent{taskCompleted: true}) {
			return ctx.Err()
		}
		body, err := json.Marshal(event.Completion)
		if err != nil {
			return err
		}
		bridged.learningName = thinker.TaskCompleteEventName
		bridged.learningPayload = body
	default:
		return nil
	}
	if !emitAgentEvent(ctx, events, bridged) {
		return ctx.Err()
	}
	return nil
}

type loopSkillHintSearcher struct {
	source skillHintSearcher
}

func appSkillHintSearcher(runtime agentToolRuntime) agentloop.SkillHintSearcher {
	source, ok := runtime.(skillHintSearcher)
	if !ok {
		return nil
	}
	return loopSkillHintSearcher{source: source}
}

func (s loopSkillHintSearcher) SearchSkillHints(
	ctx context.Context,
	query string,
	limit int,
) (agentloop.SkillSearchResult, error) {
	result, err := s.source.SearchSkillHints(ctx, query, limit)
	if err != nil {
		return agentloop.SkillSearchResult{}, err
	}
	hits := make([]agentloop.SkillSearchHit, 0, len(result.Hits))
	for _, hit := range result.Hits {
		hits = append(hits, agentloop.SkillSearchHit{
			ID: hit.ID, Title: hit.Title, Description: hit.Description,
			Tags: append([]string(nil), hit.Tags...), Scope: hit.Scope,
		})
	}
	return agentloop.SkillSearchResult{Hits: hits}, nil
}
