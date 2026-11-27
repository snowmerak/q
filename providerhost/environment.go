package providerhost

import (
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/internal/providerenv"
)

// EnvironmentNames lists the environment variables that value's providers
// read their API keys from.
func EnvironmentNames(value gateway.Config) []string {
	var names []string
	for _, provider := range value.Providers {
		if provider.APIKeyEnv != "" {
			names = append(names, provider.APIKeyEnv)
		}
	}
	return names
}

// WithRefreshedEnvironment resolves api_key_env references that have a
// refreshed value into in-memory API keys for a Gateway that runs in this
// process. The result must not be persisted. A Gateway child receives
// refreshed values through its environment instead.
func WithRefreshedEnvironment(value gateway.Config) gateway.Config {
	refreshed := providerenv.Values()
	if len(refreshed) == 0 {
		return value
	}
	value.Providers = append([]gateway.ProviderConfig(nil), value.Providers...)
	for index := range value.Providers {
		provider := &value.Providers[index]
		if provider.APIKeyEnv == "" {
			continue
		}
		if _, ok := refreshed[provider.APIKeyEnv]; !ok {
			continue
		}
		provider.APIKey = providerenv.Getenv(provider.APIKeyEnv)
		provider.APIKeyEnv = ""
	}
	return value
}
