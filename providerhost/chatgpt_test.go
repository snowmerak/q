package providerhost

import (
	"github.com/snowmerak/llm-provider/gateway"
	"path/filepath"
	"testing"
)

func TestChatGPTIdentityOwnedByQ(t *testing.T) {
	directory := t.TempDir()
	original := gateway.Config{Providers: []gateway.ProviderConfig{{ID: "plan", Type: "chatgpt", ChatGPT: gateway.ChatGPTConfig{AppID: "foreign", AppName: "Other", Directory: "copied-host"}}, {ID: "alias", Type: "chatgpt"}}}
	value := LocalConfig(original, directory)
	for _, provider := range value.Providers {
		if provider.ChatGPT.AppID != "q" || provider.ChatGPT.AppName != "Q" || provider.ChatGPT.Directory != filepath.Join(directory, "chatgpt") {
			t.Fatalf("unexpected identity: %+v", provider.ChatGPT)
		}
	}
	if original.Providers[0].ChatGPT.AppID != "foreign" {
		t.Fatal("runtime identity mutated exportable configuration")
	}
	if LocalConfig(value, directory).Providers[0].ChatGPT != value.Providers[0].ChatGPT {
		t.Fatal("restart changed identity")
	}
}
