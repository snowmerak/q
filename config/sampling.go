package config

import (
	"fmt"
	"math"
)

// SamplingConfig overrides only explicitly configured values. Pointers preserve
// zero as a setting, while nil leaves the request/provider default unchanged.
type SamplingConfig struct {
	Temperature       *float64 `yaml:"temperature,omitempty" json:"temperature,omitempty"`
	TopP              *float64 `yaml:"top_p,omitempty" json:"top_p,omitempty"`
	TopK              *int     `yaml:"top_k,omitempty" json:"top_k,omitempty"`
	MinP              *float64 `yaml:"min_p,omitempty" json:"min_p,omitempty"`
	RepetitionPenalty *float64 `yaml:"repetition_penalty,omitempty" json:"repetition_penalty,omitempty"`
	PresencePenalty   *float64 `yaml:"presence_penalty,omitempty" json:"presence_penalty,omitempty"`
	FrequencyPenalty  *float64 `yaml:"frequency_penalty,omitempty" json:"frequency_penalty,omitempty"`
}

func (s SamplingConfig) Empty() bool {
	return s.Temperature == nil && s.TopP == nil && s.TopK == nil && s.MinP == nil &&
		s.RepetitionPenalty == nil && s.PresencePenalty == nil && s.FrequencyPenalty == nil
}

func (s SamplingConfig) Validate() error {
	for _, field := range []struct {
		name     string
		value    *float64
		min, max float64
	}{
		{"temperature", s.Temperature, 0, math.Inf(1)},
		{"top_p", s.TopP, 0, 1},
		{"min_p", s.MinP, 0, 1},
		{"repetition_penalty", s.RepetitionPenalty, 0, math.Inf(1)},
		{"presence_penalty", s.PresencePenalty, -2, 2},
		{"frequency_penalty", s.FrequencyPenalty, -2, 2},
	} {
		if field.value == nil {
			continue
		}
		value := *field.value
		if math.IsNaN(value) || math.IsInf(value, 0) || value < field.min || value > field.max || (field.name == "repetition_penalty" && value == 0) {
			return fmt.Errorf("invalid %s value %v", field.name, value)
		}
	}
	if s.TopK != nil && *s.TopK < 0 {
		return fmt.Errorf("top_k must not be negative")
	}
	return nil
}
