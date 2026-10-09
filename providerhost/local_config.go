package providerhost

import (
	"maps"
	"path/filepath"
	"strings"

	"github.com/snowmerak/llm-provider/gateway"
)

// LocalConfig assigns Q's application identity at the process boundary without
// adding runtime defaults to providers.json or settings exports. ChatGPT
// generations also share one local account store and its OS refresh lock.
func LocalConfig(value gateway.Config, directory string) gateway.Config {
	value.Providers = append([]gateway.ProviderConfig(nil), value.Providers...)
	for index := range value.Providers {
		switch value.Providers[index].Type {
		case "chatgpt":
			value.Providers[index].ChatGPT = gateway.ChatGPTConfig{AppID: "q", AppName: "Q", Directory: filepath.Join(directory, "chatgpt")}
		case "openrouter":
			headers := maps.Clone(value.Providers[index].Headers)
			if headers == nil {
				headers = make(map[string]string)
			}
			setDefaultHeader(headers, "HTTP-Referer", "https://q.saturday.ne.kr")
			setDefaultHeader(headers, "X-OpenRouter-Title", "Q")
			value.Providers[index].Headers = headers
		}
	}
	return value
}

func setDefaultHeader(headers map[string]string, name, value string) {
	for configured := range headers {
		if strings.EqualFold(configured, name) {
			return
		}
	}
	headers[name] = value
}
