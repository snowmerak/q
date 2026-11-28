package gateway

import "testing"

func TestNativeProviderKindValidation(t *testing.T) {
	canonicalKinds := []string{"openai", "openrouter", "grok", "anthropic", "codex", "generic"}
	tests := []struct {
		providerType string
		allowedKind  string
	}{
		{providerType: "anthropic", allowedKind: "anthropic"},
		{providerType: "claude", allowedKind: "anthropic"},
		{providerType: "grok", allowedKind: "grok"},
		{providerType: "xai", allowedKind: "grok"},
		{providerType: "codex", allowedKind: "codex"},
		{providerType: "codex-app-server", allowedKind: "codex"},
		{providerType: "openrouter", allowedKind: "openrouter"},
	}

	for _, test := range tests {
		t.Run(test.providerType, func(t *testing.T) {
			withoutKind := validProviderConfig(test.providerType, "")
			if err := withoutKind.validate(); err != nil {
				t.Fatalf("omitted kind: %v", err)
			}

			for _, kind := range canonicalKinds {
				provider := validProviderConfig(test.providerType, kind)
				err := provider.validate()
				if kind == test.allowedKind && err != nil {
					t.Errorf("kind %q should be accepted: %v", kind, err)
				}
				if kind != test.allowedKind && err == nil {
					t.Errorf("kind %q should be rejected", kind)
				}
			}
		})
	}
}

func TestNativeProviderKindAliases(t *testing.T) {
	tests := []struct {
		providerType string
		kind         string
	}{
		{providerType: "anthropic", kind: "claude"},
		{providerType: "claude", kind: "anthropic"},
		{providerType: "grok", kind: "xai"},
		{providerType: "xai", kind: "grok"},
		{providerType: "codex", kind: "codex-app-server"},
		{providerType: "codex-app-server", kind: "codex"},
		{providerType: "openrouter", kind: "openrouter"},
	}

	for _, test := range tests {
		t.Run(test.providerType+"_as_"+test.kind, func(t *testing.T) {
			provider := validProviderConfig(test.providerType, test.kind)
			if err := provider.validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGenericKindIsOpenAICompatibleOnly(t *testing.T) {
	compatible := validProviderConfig("openai-compatible", "generic")
	if err := compatible.validate(); err != nil {
		t.Fatalf("openai-compatible generic kind: %v", err)
	}
	if kind := effectiveProviderKind(compatible); kind != "generic" {
		t.Fatalf("effective kind = %q, want generic", kind)
	}

	for _, providerType := range []string{"anthropic", "claude", "grok", "xai", "codex", "codex-app-server", "openrouter"} {
		t.Run(providerType, func(t *testing.T) {
			provider := validProviderConfig(providerType, "generic")
			if err := provider.validate(); err == nil {
				t.Fatal("generic kind was accepted")
			}
		})
	}
}

func validProviderConfig(providerType, kind string) ProviderConfig {
	return ProviderConfig{
		ID:      "test",
		Type:    providerType,
		Kind:    kind,
		Enabled: true,
		BaseURL: "https://gateway.example/v1",
		APIKey:  "test-key",
	}
}
