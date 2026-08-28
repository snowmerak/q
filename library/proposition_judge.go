package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/providerhost"
)

const (
	PropositionActionCreate  = "create"
	PropositionActionMerge   = "merge"
	PropositionActionDiscard = "discard"
)

// PropositionDecision is produced by a fresh Library-owned model session for
// one queued registration. TargetID is required for merge and discard.
type PropositionDecision struct {
	Action   string `json:"action"`
	TargetID string `json:"target_id,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type PropositionJudge interface {
	JudgeProposition(context.Context, PropositionRegisterRequest, []PropositionSearchHit) (PropositionDecision, error)
}

type propositionJudgeClient interface {
	Chat(context.Context, client.ChatRequest) (*client.ChatResponse, error)
	Close() error
}

type modelPropositionJudge struct {
	client     propositionJudgeClient
	manager    *providerhost.Manager
	model      string
	effort     string
	group      string
	candidates []client.ModelCandidate
	router     *client.ModelRouter
}

func newConfiguredPropositionJudge(ctx context.Context, dir string) (*modelPropositionJudge, error) {
	value, err := (config.Store{Dir: dir}).Load()
	if errors.Is(err, config.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	agent, err := value.EffectiveAgent(config.AgentRoleLibrarian)
	if err != nil {
		return nil, err
	}
	configuredCandidates, err := value.EffectiveModelCandidates(config.AgentRoleLibrarian)
	if err != nil {
		return nil, err
	}
	candidates := make([]client.ModelCandidate, len(configuredCandidates))
	for index, candidate := range configuredCandidates {
		candidates[index] = client.ModelCandidate{
			Model: candidate.Model, ReasoningEffort: candidate.ReasoningEffort, Timeout: candidate.Timeout,
		}
	}
	var configured *client.Client
	judge := &modelPropositionJudge{
		model: candidates[0].Model, effort: candidates[0].ReasoningEffort,
		group: agent.Group, candidates: candidates,
	}
	if value.Provider.Managed {
		manager, err := providerhost.NewManager(ctx, providerhost.Store{Dir: dir})
		if err != nil {
			return nil, err
		}
		if err := manager.LoadAndStart(ctx); err != nil {
			_ = manager.Close()
			return nil, err
		}
		configured, err = client.New(client.Config{
			BaseURL: manager.Endpoint(), APIKey: manager.APIKey(), DefaultModel: value.Provider.Model,
		})
		if err != nil {
			_ = manager.Close()
			return nil, err
		}
		judge.manager = manager
	} else {
		apiKey := value.Provider.ResolveAPIKey()
		configured, err = client.New(client.Config{
			BaseURL: value.Provider.BaseURL, APIKey: apiKey, DefaultModel: value.Provider.Model,
			DisableAPIKey: apiKey == "",
		})
		if err != nil {
			return nil, err
		}
	}
	judge.client = configured
	return judge, nil
}

func (j *modelPropositionJudge) Close() error {
	if j == nil {
		return nil
	}
	var clientErr, managerErr error
	if j.client != nil {
		clientErr = j.client.Close()
	}
	if j.manager != nil {
		managerErr = j.manager.Close()
	}
	return errors.Join(clientErr, managerErr)
}

func (j *modelPropositionJudge) JudgeProposition(
	ctx context.Context,
	proposal PropositionRegisterRequest,
	candidates []PropositionSearchHit,
) (PropositionDecision, error) {
	if j == nil || j.client == nil {
		return PropositionDecision{}, errors.New("library: proposition judge is unavailable")
	}
	// Embeddings are used for retrieval but are large derived data and do not
	// help the model compare proposition semantics.
	proposal.Embeddings = nil
	input, err := json.Marshal(struct {
		Proposal   PropositionRegisterRequest `json:"proposal"`
		Candidates []PropositionSearchHit     `json:"candidates"`
	}{Proposal: proposal, Candidates: candidates})
	if err != nil {
		return PropositionDecision{}, err
	}
	strict := true
	parallel := false
	request := client.ChatRequest{
		Model: j.model, ReasoningEffort: j.effort,
		Messages: []client.Message{
			{Role: client.RoleSystem, Content: propositionJudgeInstructions},
			{Role: client.RoleUser, Content: "Judge this proposition registration against the retrieved candidates:\n" + string(input)},
		},
		Tools: []client.Tool{{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
			Name: "resolve_proposition", Description: "Choose exactly one final disposition for the proposed proposition.", Strict: &strict,
			Parameters: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"action":    map[string]any{"type": "string", "enum": []string{PropositionActionCreate, PropositionActionMerge, PropositionActionDiscard}},
					"target_id": map[string]any{"type": "string"},
					"reason":    map[string]any{"type": "string", "maxLength": 1024},
				},
				"required": []string{"action", "target_id", "reason"},
			},
		}}},
		ToolChoice: client.ToolChoiceRequired, ParallelToolCalls: &parallel,
	}
	var response *client.ChatResponse
	selected := 0
	if j.group == "" {
		response, err = j.client.Chat(ctx, request)
	} else {
		response, selected, err = j.router.RouteChat(ctx, j.client, request, j.candidates, 0)
	}
	if err != nil {
		return PropositionDecision{}, fmt.Errorf("library: judge proposition: %w", err)
	}
	if response == nil || len(response.Choices) == 0 || len(response.Choices[0].Message.ToolCalls) != 1 {
		return PropositionDecision{}, errors.New("library: proposition judge must call resolve_proposition exactly once")
	}
	call := response.Choices[0].Message.ToolCalls[0]
	if call.Function.Name != "resolve_proposition" {
		return PropositionDecision{}, fmt.Errorf("library: proposition judge called unsupported tool %q", call.Function.Name)
	}
	var decision PropositionDecision
	decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decision); err != nil {
		return PropositionDecision{}, fmt.Errorf("library: decode proposition decision: %w", err)
	}
	decision.Action = strings.TrimSpace(decision.Action)
	decision.TargetID = strings.TrimSpace(decision.TargetID)
	decision.Reason = strings.TrimSpace(decision.Reason)
	if err := validatePropositionDecision(decision, candidates); err != nil {
		return PropositionDecision{}, err
	}
	assistant := response.Choices[0].Message
	if assistant.Role == "" {
		assistant.Role = client.RoleAssistant
	}
	body, _ := json.Marshal(decision)
	request.Messages = append(request.Messages, assistant, client.ToolResultMessage(call, client.ToolResult{Content: string(body)}))
	request.ConversationID = response.ConversationID
	if j.group != "" {
		request.Model = j.candidates[selected].Model
		request.ReasoningEffort = j.candidates[selected].ReasoningEffort
	}
	if _, err := client.FinishToolTurn(ctx, request, func(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		if j.group != "" && j.candidates[selected].Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, j.candidates[selected].Timeout)
			defer cancel()
		}
		return j.client.Chat(ctx, request)
	}, nil); err != nil {
		return PropositionDecision{}, fmt.Errorf("library: judge proposition: %w", err)
	}
	return decision, nil
}

func validatePropositionDecision(decision PropositionDecision, candidates []PropositionSearchHit) error {
	switch decision.Action {
	case PropositionActionCreate:
		if decision.TargetID != "" {
			return errors.New("library: create decision must not select a target")
		}
		return nil
	case PropositionActionMerge, PropositionActionDiscard:
		if decision.TargetID == "" {
			return fmt.Errorf("library: %s decision requires a target", decision.Action)
		}
		for _, candidate := range candidates {
			if candidate.ID == decision.TargetID {
				return nil
			}
		}
		return fmt.Errorf("library: proposition decision selected unknown candidate %q", decision.TargetID)
	default:
		return fmt.Errorf("library: unsupported proposition decision %q", decision.Action)
	}
}

const propositionJudgeInstructions = `You are q Library's proposition deduplication judge.
The proposal is a durable fact extracted from one workspace conversation. Candidates are existing global propositions retrieved by hybrid BM25/vector relevance with recency disabled. The score is only a ranking hint.
Choose create when the proposal has a distinct truth condition, scope, subject, constraint, or time meaning. Choose merge only when it expresses the same durable fact and the new evidence should reinforce the existing proposition. Choose discard only when it is the same fact and adds no useful provenance or retrieval value.
High semantic similarity does not imply equivalence: distinguish negation, changed preferences, superseding facts, different subjects, and narrower or broader conditions. When candidates are absent, choose create.
Call resolve_proposition exactly once and never answer with plain text.`
