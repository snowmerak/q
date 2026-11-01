package providerhost

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

// ModelGenerationSettings resolves model behavior without assuming that an
// autonomous Codex/ChatGPT backend can safely replay a partially executed turn.
func ModelGenerationSettings(providers gateway.Config, value config.Config, model string) client.GenerationSettings {
	settings := client.GenerationSettings{MaxTokens: value.ModelOutputLimits[model], TokenField: value.ModelOutputTokenFields[model], MaxAttempts: value.GenerationRecovery.Attempts(), RequireFinishReason: !value.ModelAllowMissingFinishReason[model]}
	if group, ok := strings.CutPrefix(model, "group/"); ok {
		candidates := value.ModelGroups[group].Candidates
		settings.SafeReplay = len(candidates) > 0
		for _, candidate := range candidates {
			settings.SafeReplay = settings.SafeReplay && ModelGenerationSettings(providers, value, candidate.Model).SafeReplay
		}
		return settings
	}
	for _, p := range providers.Providers {
		prefix := p.Prefix
		if prefix == "" {
			prefix = p.ID
		}
		if !strings.HasPrefix(model, prefix+"/") {
			continue
		}
		switch p.Type {
		case "chatgpt", "codex":
			settings.MaxAttempts = 0
		case "openai-compatible", "openai", "openrouter", "xai", "grok", "anthropic", "claude":
			settings.SafeReplay = true
		}
		return settings
	}
	return settings
}

func outputLimitSupported(providers gateway.Config, model string) bool {
	for _, p := range providers.Providers {
		prefix := p.Prefix
		if prefix == "" {
			prefix = p.ID
		}
		if strings.HasPrefix(model, prefix+"/") {
			return p.Type != "chatgpt" && p.Type != "codex"
		}
	}
	return true
}

// ValidateOutputLimits checks the provider metadata available offline. Unknown
// upstream limits remain unknown and are validated by the provider on use.
func ValidateOutputLimits(providers gateway.Config, value config.Config) error {
	for model, limit := range value.ModelOutputLimits {
		if !outputLimitSupported(providers, model) {
			return fmt.Errorf("model %q does not support an output limit", model)
		}
		for _, p := range providers.Providers {
			prefix := p.Prefix
			if prefix == "" {
				prefix = p.ID
			}
			if upstream, found := strings.CutPrefix(model, prefix+"/"); found {
				if maximum := p.ModelMetadata[upstream].MaxOutputTokens; maximum > 0 && int64(limit) > maximum {
					return fmt.Errorf("output limit for %q exceeds the model maximum %d", model, maximum)
				}
			}
		}
	}
	return nil
}

func (h *modelGroupHandler) applyGenerationLimit(writer http.ResponseWriter, fields map[string]json.RawMessage, value config.Config, model string, responses bool, snapshot string) error {
	limit, field := value.ModelOutputLimits[model], value.ModelOutputTokenFields[model]
	allowMissing := value.ModelAllowMissingFinishReason[model]
	if snapshot != "" {
		var pinned client.OutputLimitSnapshot
		if err := json.Unmarshal([]byte(snapshot), &pinned); err != nil || pinned.Model != model || pinned.Limit < 0 || !hmac.Equal([]byte(pinned.Signature), []byte(h.signOutputSnapshot(pinned))) {
			return fmt.Errorf("invalid generation snapshot")
		}
		limit, field = pinned.Limit, pinned.Field
		allowMissing = pinned.AllowMissingFinishReason
	}
	for _, name := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if raw := fields[name]; len(raw) > 0 && string(raw) != "null" {
			var requested int
			if err := json.Unmarshal(raw, &requested); err != nil || requested <= 0 {
				return fmt.Errorf("%s must be a positive integer", name)
			}
			if limit <= 0 || requested < limit {
				limit = requested
			}
		}
	}
	if limit > 0 {
		providers, err := (Store{Dir: h.store.Dir}).Load()
		if err == nil && !outputLimitSupported(providers, model) {
			return fmt.Errorf("model %q does not support an output limit", model)
		}
		for _, name := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
			delete(fields, name)
		}
		name := "max_completion_tokens"
		if field == "max_tokens" {
			name = field
		}
		if responses {
			name = "max_output_tokens"
		}
		fields[name], _ = json.Marshal(limit)
	}
	result := client.OutputLimitSnapshot{Model: model, Limit: limit, Field: field, AllowMissingFinishReason: allowMissing}
	_ = json.Unmarshal(fields["reasoning_effort"], &result.ReasoningEffort)
	if responses {
		var reasoning struct {
			Effort string `json:"effort"`
		}
		if json.Unmarshal(fields["reasoning"], &reasoning) == nil {
			result.ReasoningEffort = reasoning.Effort
		}
	}
	result.Signature = h.signOutputSnapshot(result)
	encoded, _ := json.Marshal(result)
	writer.Header().Set(client.OutputLimitSnapshotHeader, string(encoded))
	return nil
}

func (h *modelGroupHandler) signOutputSnapshot(snapshot client.OutputLimitSnapshot) string {
	mac := hmac.New(sha256.New, []byte(h.snapshotKey))
	_, _ = fmt.Fprintf(mac, "%s\x00%d\x00%s\x00%t\x00%s", snapshot.Model, snapshot.Limit, snapshot.Field, snapshot.AllowMissingFinishReason, snapshot.ReasoningEffort)
	return hex.EncodeToString(mac.Sum(nil))
}
