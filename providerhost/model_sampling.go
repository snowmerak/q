package providerhost

import (
	"encoding/json"
	"maps"

	"github.com/snowmerak/q/config"
)

// applySampling preserves omitted request fields and forwards explicit zeroes.
// This runs after candidate selection so overrides cannot leak across fallbacks.
func applySampling(fields map[string]json.RawMessage, sampling config.SamplingConfig) error {
	body, err := json.Marshal(sampling)
	if err != nil {
		return err
	}
	var overrides map[string]json.RawMessage
	if err := json.Unmarshal(body, &overrides); err != nil {
		return err
	}
	maps.Copy(fields, overrides)
	return nil
}
