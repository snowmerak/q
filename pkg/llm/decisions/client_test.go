package decisions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/snowmerak/decisions"
)

func TestConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{
			name: "duplicate provider",
			cfg: Config{
				Providers: []ProviderConfig{
					{ID: "p1", Protocol: "openai", BaseURL: "http://127.0.0.1:8080/v1"},
					{ID: "p1", Protocol: "openai", BaseURL: "http://127.0.0.1:8081/v1"},
				},
			},
		},
		{
			name: "unknown protocol",
			cfg: Config{
				Providers: []ProviderConfig{
					{ID: "p1", Protocol: "unknown", BaseURL: "http://127.0.0.1:8080/v1"},
				},
			},
		},
		{
			name: "missing provider ref",
			cfg: Config{
				Providers: []ProviderConfig{{ID: "p1", Protocol: "openai", BaseURL: "http://127.0.0.1:8080/v1"}},
				Models:    []ModelConfig{{ID: "m1", ProviderID: "missing", Model: "gpt-6"}},
			},
		},
		{
			name: "unknown default model",
			cfg: Config{
				Providers:      []ProviderConfig{{ID: "p1", Protocol: "openai", BaseURL: "http://127.0.0.1:8080/v1"}},
				Models:         []ModelConfig{{ID: "m1", ProviderID: "p1", Model: "gpt-6"}},
				DefaultModelID: "m2",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); !errors.Is(err, ErrInvalidCfg) {
				t.Fatalf("expected ErrInvalidCfg, got %v", err)
			}
		})
	}
}

func TestRegistryRequiresEnvKeyWhenConfigured(t *testing.T) {
	cfg := Config{
		Providers: []ProviderConfig{
			{ID: "p1", Protocol: "openai", BaseURL: "http://127.0.0.1:8080/v1", APIKeyEnv: "DECISION_TEST_KEY"},
		},
		Models: []ModelConfig{{ID: "m1", ProviderID: "p1", Model: "gpt-6"}},
	}
	_, err := NewRegistry(cfg, func(string) string { return "" })
	if !errors.Is(err, ErrInvalidCfg) {
		t.Fatalf("expected ErrInvalidCfg when env key empty, got %v", err)
	}
}

func TestRegistryResolveDoesNotFallbackOnUnknownModel(t *testing.T) {
	cfg := Config{
		Providers:      []ProviderConfig{{ID: "p1", Protocol: "openai", BaseURL: "http://127.0.0.1:8080/v1"}},
		Models:         []ModelConfig{{ID: "m1", ProviderID: "p1", Model: "gpt-6"}},
		DefaultModelID: "m1",
	}
	reg, err := NewRegistry(cfg, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if _, _, err := reg.Resolve(""); err != nil {
		t.Fatalf("Resolve default: %v", err)
	}
	if _, _, err := reg.Resolve("removed-model"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("expected ErrUnknownModel, got %v", err)
	}
}

func TestEvaluateAcrossProtocols(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/decisions":
			if got := r.Header.Get("x-goog-api-key"); got != "" {
				// Gemini Decisions endpoint
				if got != "gem-secret" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"usage": map[string]any{"total_input_tokens": 10, "total_output_tokens": 2},
					"answers": map[string]any{
						"allow": map[string]any{"type": "noul", "noul": 0.8, "confidence": 0.9},
					},
				})
				return
			}
			// OpenAI Decisions endpoint
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model": "gpt-6",
				"usage": map[string]any{"input_tokens": 12, "output_tokens": 3},
				"answers": []map[string]any{
					{"name": "allow", "type": "predicate", "probability": 0.75},
				},
			})
		case "/v1/systemone":
			if r.Header.Get("Authorization") != "Bearer jev-secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model": "jev-latest",
				"usage": map[string]any{"input_tokens": 8, "output_tokens": 2},
				"answers": map[string]any{
					"allow": map[string]any{"type": "noul", "noul": 0.2},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	env := map[string]string{
		"JEV_KEY": "jev-secret",
		"GEM_KEY": "gem-secret",
	}
	reg, err := NewRegistry(Config{
		Providers: []ProviderConfig{
			{ID: "oa", Label: "OpenAI", Protocol: "openai", BaseURL: srv.URL + "/v1"},
			{ID: "jv", Label: "JeV", Protocol: "jev", BaseURL: srv.URL + "/v1", APIKeyEnv: "JEV_KEY"},
			{ID: "gm", Label: "Gemini", Protocol: "gemini", BaseURL: srv.URL + "/v1", APIKeyEnv: "GEM_KEY"},
		},
		Models: []ModelConfig{
			{ID: "m-oa", ProviderID: "oa", Model: "gpt-6", ContextWindow: 32768},
			{ID: "m-jv", ProviderID: "jv", Model: "jev-latest", ContextWindow: 128000},
			{ID: "m-gm", ProviderID: "gm", Model: "gemini-decision-1", ContextWindow: 262144},
		},
		DefaultModelID: "m-oa",
	}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	if got := len(reg.ListModels()); got != 3 {
		t.Fatalf("expected 3 models, got %d", got)
	}

	q := sdk.Questions{
		"allow": sdk.NoulQuestion{Instructions: "Allow this tool call?"},
	}
	in := sdk.Input{State: "read_file README.md"}

	for _, tc := range []struct {
		modelID  string
		wantPass bool
	}{
		{modelID: "", wantPass: true},
		{modelID: "m-jv", wantPass: false},
		{modelID: "m-gm", wantPass: true},
	} {
		resp, err := reg.Evaluate(context.Background(), tc.modelID, in, q)
		if err != nil {
			t.Fatalf("Evaluate(%q): %v", tc.modelID, err)
		}
		pass, err := Positive(resp, "allow")
		if err != nil {
			t.Fatalf("Positive(%q): %v", tc.modelID, err)
		}
		if pass != tc.wantPass {
			t.Fatalf("Evaluate(%q) pass=%v, want %v", tc.modelID, pass, tc.wantPass)
		}
	}
}
