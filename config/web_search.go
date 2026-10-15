package config

import (
	"errors"
	"strings"
)

// WebSearchConfig controls browser capacity and reserves an explicit model for
// future generative search planning. It never inherits the active chat model.
type WebSearchConfig struct {
	MaxConcurrentReads int    `yaml:"max_concurrent_reads,omitempty" json:"max_concurrent_reads"`
	GenerativeModel    string `yaml:"generative_model,omitempty" json:"generative_model"`
}

func (c WebSearchConfig) Effective() WebSearchConfig {
	if c.MaxConcurrentReads == 0 {
		c.MaxConcurrentReads = 2
	}
	return c
}

func (c WebSearchConfig) Validate() error {
	if c.MaxConcurrentReads < 0 || c.MaxConcurrentReads > 8 {
		return errors.New("web_search: max_concurrent_reads must be between 1 and 8 (0 uses default 2)")
	}
	if c.GenerativeModel != "" {
		provider, model, ok := strings.Cut(c.GenerativeModel, "/")
		if !ok || provider == "" || model == "" || strings.HasPrefix(c.GenerativeModel, "group/") || strings.ContainsAny(c.GenerativeModel, " \t\r\n") {
			return errors.New("web_search: generative_model must be a concrete provider/model ID")
		}
	}
	return nil
}
