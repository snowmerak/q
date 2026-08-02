package providerhost

import (
	"errors"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
)

func TestStoreRoundTrip(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if _, err := store.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load() error = %v, want ErrNotFound", err)
	}
	value := gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "local", Type: "openai-compatible", Enabled: true,
		BaseURL: "http://127.0.0.1:1234/v1", APIKeyEnv: "LOCAL_API_KEY",
	}}}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Listen != "127.0.0.1:0" || len(loaded.Providers) != 1 ||
		loaded.Providers[0].ID != "local" || loaded.Providers[0].APIKeyEnv != "LOCAL_API_KEY" {
		t.Fatalf("loaded config = %#v", loaded)
	}
}
