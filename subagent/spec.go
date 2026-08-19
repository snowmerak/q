// Package subagent resolves role-specific execution settings.
package subagent

import (
	"context"
	"fmt"
	"slices"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

// Spec is the effective model configuration for one subagent execution.
// Reasoning describes model-list metadata for callers that need to present
// supported controls in a UI.
type Spec struct {
	Role            string
	Group           string
	Model           string
	ReasoningEffort string
	Reasoning       *client.ReasoningCapabilities
	ContextLength   int64
	MaxOutputTokens int64
	Candidates      []client.ModelCandidate
	Router          *client.ModelRouter
	activeCandidate int
	conversationID  string
}

// Resolve combines a role override with the active chat model and validates
// an explicit reasoning effort against /v1/models metadata.
func Resolve(value config.Config, role string, models []client.Model) (Spec, error) {
	agent, err := value.EffectiveAgent(role)
	if err != nil {
		return Spec{}, err
	}
	configured, err := value.EffectiveModelCandidates(role)
	if err != nil {
		return Spec{}, err
	}
	candidates := make([]client.ModelCandidate, 0, len(configured))
	var primary client.Model
	var minimumContext, minimumOutput int64
	contextKnown := len(configured) > 0
	outputKnown := len(configured) > 0
	for index, candidate := range configured {
		model, found := findModel(models, candidate.Model)
		if !found {
			return Spec{}, fmt.Errorf("subagent: model %q for role %q was not found in /v1/models", candidate.Model, role)
		}
		if err := validateReasoning(fmt.Sprintf("role %q", role), candidate.Model, candidate.ReasoningEffort, model); err != nil {
			return Spec{}, err
		}
		if index == 0 {
			primary = model
		}
		if model.ContextLength <= 0 {
			contextKnown = false
		} else {
			minimumContext = minimumPositive(minimumContext, model.ContextLength)
		}
		if model.MaxOutputTokens <= 0 {
			outputKnown = false
		} else {
			minimumOutput = minimumPositive(minimumOutput, model.MaxOutputTokens)
		}
		candidates = append(candidates, client.ModelCandidate{
			Model: candidate.Model, ReasoningEffort: candidate.ReasoningEffort, Timeout: candidate.Timeout,
		})
	}
	if len(candidates) == 0 {
		return Spec{}, fmt.Errorf("subagent: role %q resolved no model candidates", role)
	}
	if !contextKnown {
		minimumContext = 0
	}
	if !outputKnown {
		minimumOutput = 0
	}
	return Spec{
		Role: role, Group: agent.Group,
		Model: candidates[0].Model, ReasoningEffort: candidates[0].ReasoningEffort,
		Reasoning: cloneReasoning(primary), ContextLength: minimumContext, MaxOutputTokens: minimumOutput,
		Candidates: candidates,
	}, nil
}

// ValidateModelGroup validates model availability and per-candidate reasoning
// controls without assigning the group to an agent role.
func ValidateModelGroup(name string, group config.ModelGroupConfig, models []client.Model) error {
	if len(group.Candidates) == 0 {
		return fmt.Errorf("subagent: model group %q has no candidates", name)
	}
	subject := fmt.Sprintf("model group %q", name)
	for _, candidate := range group.Candidates {
		model, found := findModel(models, candidate.Model)
		if !found {
			return fmt.Errorf("subagent: model %q for %s was not found in /v1/models", candidate.Model, subject)
		}
		if err := validateReasoning(subject, candidate.Model, candidate.ReasoningEffort, model); err != nil {
			return err
		}
	}
	return nil
}

func validateReasoning(subject, modelID, effort string, model client.Model) error {
	if effort == "" {
		return nil
	}
	reasoning := cloneReasoning(model)
	if reasoning == nil || !reasoning.Supported {
		return fmt.Errorf("subagent: model %q for %s does not advertise reasoning support", modelID, subject)
	}
	if reasoning.Control != client.ReasoningControlEffort {
		return fmt.Errorf("subagent: model %q for %s reasoning control is %q, not effort", modelID, subject, reasoning.Control)
	}
	if len(reasoning.SupportedEfforts) > 0 && !slices.Contains(reasoning.SupportedEfforts, effort) {
		return fmt.Errorf(
			"subagent: model %q for %s does not support reasoning effort %q (supported: %v)",
			modelID, subject, effort, reasoning.SupportedEfforts,
		)
	}
	return nil
}

// Apply sets the role-specific request controls. An empty ReasoningEffort is
// preserved so the provider uses the model's advertised default.
func (s Spec) Apply(request *client.ChatRequest) {
	index := s.activeCandidate
	if index < 0 || index >= len(s.Candidates) {
		request.Model = s.Model
		request.ReasoningEffort = s.ReasoningEffort
		return
	}
	request.Model = s.Candidates[index].Model
	request.ReasoningEffort = s.Candidates[index].ReasoningEffort
}

// Chat applies this execution's selected model and performs ordered transient
// fallback for grouped roles. Once fallback succeeds, the execution remains on
// that candidate for subsequent model rounds.
func (s *Spec) Chat(ctx context.Context, configured client.ModelChatClient, request client.ChatRequest) (*client.ChatResponse, error) {
	if s == nil {
		return nil, fmt.Errorf("subagent: model spec is nil")
	}
	if request.ConversationID == "" {
		request.ConversationID = s.conversationID
	}
	if s.Group == "" {
		s.Apply(&request)
		response, err := configured.Chat(ctx, request)
		if err == nil && response != nil && response.ConversationID != "" {
			s.conversationID = response.ConversationID
		}
		return response, err
	}
	previousCandidate := s.activeCandidate
	response, selected, err := s.Router.RouteChat(ctx, configured, request, s.Candidates, s.activeCandidate)
	if err == nil {
		s.activeCandidate = selected
		s.Model = s.Candidates[selected].Model
		s.ReasoningEffort = s.Candidates[selected].ReasoningEffort
		if response != nil && response.ConversationID != "" {
			s.conversationID = response.ConversationID
		} else if selected != previousCandidate {
			// A fallback starts a new backend lifecycle. Never retain the prior
			// candidate's stateful conversation ID when the replacement backend
			// did not return its own ID.
			s.conversationID = ""
		}
	}
	return response, err
}

func minimumPositive(current, candidate int64) int64 {
	if candidate <= 0 {
		return current
	}
	if current == 0 || candidate < current {
		return candidate
	}
	return current
}

func findModel(models []client.Model, id string) (client.Model, bool) {
	for _, model := range models {
		if model.ID == id {
			return model, true
		}
	}
	return client.Model{}, false
}

func cloneReasoning(model client.Model) *client.ReasoningCapabilities {
	if model.Capabilities == nil || model.Capabilities.Reasoning == nil {
		return nil
	}
	result := *model.Capabilities.Reasoning
	result.SupportedEfforts = append([]string(nil), result.SupportedEfforts...)
	if result.DefaultEnabled != nil {
		value := *result.DefaultEnabled
		result.DefaultEnabled = &value
	}
	return &result
}
