package config

import (
	"fmt"
	"strings"
)

// GenerationRecoveryConfig bounds additional model generations, excluding
// transport retries. Nil inherits the default; an explicit zero disables them.
type GenerationRecoveryConfig struct {
	MaxAttempts *int `yaml:"max_attempts,omitempty" json:"max_attempts,omitempty"`
}

func (c GenerationRecoveryConfig) Attempts() int {
	if c.MaxAttempts == nil {
		return 2
	}
	return *c.MaxAttempts
}

func (c Config) validateGeneration() error {
	if n := c.GenerationRecovery.Attempts(); n < 0 || n > 5 {
		return fmt.Errorf("generation_recovery.max_attempts must be between 0 and 5")
	}
	for model := range c.ModelAllowMissingFinishReason {
		if model == "" || strings.TrimSpace(model) != model || strings.HasPrefix(model, "group/") {
			return fmt.Errorf("model_allow_missing_finish_reason requires concrete model IDs")
		}
	}
	for model, limit := range c.ModelOutputLimits {
		if model == "" || strings.TrimSpace(model) != model || strings.HasPrefix(model, "group/") || limit <= 0 {
			return fmt.Errorf("model_output_limits requires concrete model IDs and positive token counts")
		}
	}
	for model, field := range c.ModelOutputTokenFields {
		if model == "" || strings.TrimSpace(model) != model || strings.HasPrefix(model, "group/") || field != "max_tokens" && field != "max_completion_tokens" {
			return fmt.Errorf("model_output_token_fields requires concrete model IDs and max_tokens or max_completion_tokens")
		}
	}
	return nil
}
