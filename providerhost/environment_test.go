package providerhost

import (
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/internal/providerenv"
)

func TestWithRefreshedEnvironmentResolvesOnlyRefreshedNames(t *testing.T) {
	previous := providerenv.Replace(map[string]string{"Q_TEST_ROTATED": "rotated"})
	defer providerenv.Replace(previous)
	value := gateway.Config{Providers: []gateway.ProviderConfig{
		{ID: "rotated", APIKeyEnv: "Q_TEST_ROTATED"},
		{ID: "inherited", APIKeyEnv: "Q_TEST_INHERITED"},
	}}
	resolved := WithRefreshedEnvironment(value)
	if got := resolved.Providers[0]; got.APIKey != "rotated" || got.APIKeyEnv != "" {
		t.Fatalf("rotated provider = %+v", got)
	}
	if got := resolved.Providers[1]; got.APIKey != "" || got.APIKeyEnv != "Q_TEST_INHERITED" {
		t.Fatalf("inherited provider = %+v", got)
	}
	if value.Providers[0].APIKeyEnv != "Q_TEST_ROTATED" || value.Providers[0].APIKey != "" {
		t.Fatal("the input configuration was modified")
	}
	if names := EnvironmentNames(value); len(names) != 2 {
		t.Fatalf("names = %v", names)
	}
}
