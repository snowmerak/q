package providerhost

import (
	"path/filepath"

	"github.com/snowmerak/llm-provider/gateway"
)

// LocalConfig assigns Q's application identity at the process boundary. Tokens
// and installation host IDs never enter providers.json or settings exports.
// All Q Gateway generations share this local store and its OS refresh lock.
func LocalConfig(value gateway.Config, directory string) gateway.Config {
	value.Providers = append([]gateway.ProviderConfig(nil), value.Providers...)
	for index := range value.Providers {
		if value.Providers[index].Type == "chatgpt" {
			value.Providers[index].ChatGPT = gateway.ChatGPTConfig{AppID: "q", AppName: "Q", Directory: filepath.Join(directory, "chatgpt")}
		}
	}
	return value
}
