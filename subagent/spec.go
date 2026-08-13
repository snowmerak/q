// Package subagent resolves role-specific execution settings.
package subagent

import (
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
	Model           string
	ReasoningEffort string
	Reasoning       *client.ReasoningCapabilities
	ContextLength   int64
	MaxOutputTokens int64
}

// Resolve combines a role override with the active chat model and validates
// an explicit reasoning effort against /v1/models metadata.
func Resolve(value config.Config, role string, models []client.Model) (Spec, error) {
	agent, err := value.EffectiveAgent(role)
	if err != nil {
		return Spec{}, err
	}

	model, found := findModel(models, agent.Model)
	if !found {
		return Spec{}, fmt.Errorf("subagent: model %q for role %q was not found in /v1/models", agent.Model, role)
	}

	spec := Spec{
		Role:            role,
		Model:           agent.Model,
		ReasoningEffort: agent.ReasoningEffort,
		Reasoning:       cloneReasoning(model),
		ContextLength:   model.ContextLength,
		MaxOutputTokens: model.MaxOutputTokens,
	}
	if agent.ReasoningEffort == "" {
		return spec, nil
	}
	if spec.Reasoning == nil || !spec.Reasoning.Supported {
		return Spec{}, fmt.Errorf("subagent: model %q does not advertise reasoning support", agent.Model)
	}
	if spec.Reasoning.Control != client.ReasoningControlEffort {
		return Spec{}, fmt.Errorf("subagent: model %q reasoning control is %q, not effort", agent.Model, spec.Reasoning.Control)
	}
	if len(spec.Reasoning.SupportedEfforts) > 0 &&
		!slices.Contains(spec.Reasoning.SupportedEfforts, agent.ReasoningEffort) {
		return Spec{}, fmt.Errorf(
			"subagent: model %q does not support reasoning effort %q (supported: %v)",
			agent.Model, agent.ReasoningEffort, spec.Reasoning.SupportedEfforts,
		)
	}
	return spec, nil
}

// Apply sets the role-specific request controls. An empty ReasoningEffort is
// preserved so the provider uses the model's advertised default.
func (s Spec) Apply(request *client.ChatRequest) {
	request.Model = s.Model
	request.ReasoningEffort = s.ReasoningEffort
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
