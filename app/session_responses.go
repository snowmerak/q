package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/snowmerak/q/agentinstructions"
	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/workspace"
)

type SessionResponseChange struct {
	Index     int
	Action    string
	Content   string
	UpdatedAt time.Time
}

// ReviseResponse serializes with ordinary turns and preserves the original
// response until its replacement is ready to save.
func (host *SessionHost) ReviseResponse(ctx context.Context, store workspace.Store, change SessionResponseChange) (detail string, returnErr error) {
	if err := store.RequireInteraction(); err != nil {
		return "", err
	}
	if err := host.ReleaseSession(store, store.SessionID); err != nil {
		return "", err
	}

	var prepared *preparedSession
	if change.Action == "regenerate" {
		selection, err := store.LoadSessionModelConfig()
		if err != nil {
			return "", err
		}
		if selection.Agent != "" {
			return "", errors.New("response regeneration requires an API model")
		}
		prepared, err = host.prepareSession(ctx, workspace.Store{Root: store.Root}, store.SessionID)
		if err != nil {
			return "", err
		}
		defer func() { returnErr = errors.Join(returnErr, prepared.Close()) }()
	} else {
		lock, err := store.AcquireLock("q studio response change")
		if err != nil {
			return "", err
		}
		defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	}

	value, err := store.Load()
	if err != nil {
		return "", err
	}
	updatedAt := time.Time{}
	if value.UpdatedAt != nil {
		updatedAt = *value.UpdatedAt
	}
	if !updatedAt.Equal(change.UpdatedAt) {
		return "", errors.New("session history changed; refresh the conversation and try again")
	}
	if change.Index < 0 || change.Index >= len(value.Transcript) {
		return "", errors.New("the selected response is unavailable")
	}
	target := value.Transcript[change.Index]
	if target.Role != client.RoleAssistant || len(target.ToolCalls) != 0 || strings.TrimSpace(target.TextContent()) == "" {
		return "", errors.New("only saved assistant replies with text can be changed")
	}

	var usage *workspace.TokenUsage
	switch change.Action {
	case "edit":
		if strings.TrimSpace(change.Content) == "" {
			return "", errors.New("response text is required")
		}
		detail = "Response edited"
	case "delete":
		detail = "Response deleted"
	case "regenerate":
		change.Content, usage, err = generateReplacementResponse(ctx, prepared, value.Transcript[:change.Index])
		if err != nil {
			return "", err
		}
		detail = "Response regenerated"
	default:
		return "", errors.New("unsupported response action")
	}

	revision, err := workspace.NewSessionID()
	if err != nil {
		return "", err
	}
	reviseSavedResponse(&value, change, usage)
	now := time.Now().UTC()
	value.UpdatedAt, value.HistoryRevision = &now, revision
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return detail, store.Save(value)
}

func generateReplacementResponse(ctx context.Context, prepared *preparedSession, prefix []client.Message) (string, *workspace.TokenUsage, error) {
	m := prepared.state
	cfg := m.activeConfig()
	model, effort := cfg.Provider.Model, cfg.Provider.EffectiveReasoningEffort()
	if _, isACP := prepared.client.(*acpRemoteClient); isACP {
		return "", nil, errors.New("response regeneration requires an API model")
	}
	hasPrompt := false
	for _, message := range prefix {
		hasPrompt = hasPrompt || message.Role == client.RoleUser
	}
	if !hasPrompt {
		return "", nil, errors.New("the original prompt is unavailable")
	}
	var instructions []client.Message
	if cfg.Provider.SystemPrompt != "" {
		instructions = append(instructions, client.Message{Role: client.RoleSystem, Content: cfg.Provider.SystemPrompt})
	}
	instructions = append(instructions, client.Message{
		Role: client.RoleSystem, Content: "Write a replacement answer to the last user message using the conversation and any tool results already present.",
	})
	history := agentloop.NewContext(memoryPolicy(cfg), mergeWorkspaceMessages(instructions, prefix), nil)
	if prepared.projectContext != nil {
		ctx = client.WithUsageProject(ctx, prepared.projectContext.ProjectID, prepared.projectContext.ProjectName)
	}
	if _, err := history.CompactIfNeeded(ctx, prepared.client, model, effort); err != nil {
		return "", nil, err
	}
	coalesce := modelNeedsSystemInstructionCoalescing(m.gatewayConfig, cfg.ModelGroups, model, nil)
	response, err := chatWithEmptyResponseRecovery(ctx, prepared.client, client.ChatRequest{
		Model: model, Messages: providerMessages(agentinstructions.Normalize(history.Messages()), coalesce),
		ReasoningEffort: effort, Extra: m.anthropicRequestExtra(model), WorkingDirectory: prepared.executionRoot,
	})
	if err != nil {
		return "", nil, err
	}
	if response == nil || len(response.Choices) == 0 {
		return "", nil, errors.New("the model did not return a replacement reply")
	}
	message := response.Choices[0].Message
	if len(message.ToolCalls) != 0 || strings.TrimSpace(message.TextContent()) == "" {
		return "", nil, fmt.Errorf("model %s did not return a text reply", model)
	}
	return message.TextContent(), responseTokenUsage(response.Usage), nil
}

func reviseSavedResponse(value *workspace.Session, change SessionResponseChange, newUsage *workspace.TokenUsage) {
	ordinal := 0
	for _, message := range value.Transcript[:change.Index] {
		if message.Role == client.RoleAssistant {
			ordinal++
		}
	}
	deleting := change.Action == "delete"
	if deleting {
		value.Transcript = append(value.Transcript[:change.Index], value.Transcript[change.Index+1:]...)
	} else {
		previous := value.Transcript[change.Index]
		replacement := client.Message{Role: client.RoleAssistant, Content: change.Content}
		if change.Action == "edit" && len(previous.ContentParts) > 0 {
			replacement.ContentParts = []client.MessageContentPart{{"type": "text", "text": change.Content}}
			for _, part := range previous.ContentParts {
				switch kind, _ := part["type"].(string); kind {
				case "text", "input_text", "output_text":
				default:
					replacement.ContentParts = append(replacement.ContentParts, part)
				}
			}
		}
		value.Transcript[change.Index] = replacement
	}

	usages := value.ResponseUsage[:0]
	for _, usage := range value.ResponseUsage {
		if usage.AssistantIndex == ordinal {
			continue
		}
		if deleting && usage.AssistantIndex > ordinal {
			usage.AssistantIndex--
		}
		usages = append(usages, usage)
	}
	if newUsage != nil {
		usages = append(usages, workspace.ResponseUsage{AssistantIndex: ordinal, TokenUsage: *newUsage})
	}
	value.ResponseUsage = usages

	thoughts := value.TranscriptThoughts[:0]
	for _, thought := range value.TranscriptThoughts {
		if thought.Before == change.Index {
			continue
		}
		if deleting && thought.Before > change.Index {
			thought.Before--
		}
		thoughts = append(thoughts, thought)
	}
	value.TranscriptThoughts = thoughts
	// Compacted summaries and provider IDs may still refer to the old reply.
	value.Context, value.ResponseReplay, value.ResponseAffinity = nil, nil, nil
}
