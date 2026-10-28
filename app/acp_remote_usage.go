package app

import (
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

// ACP's inputTokens counts fresh input; cache reads and writes are separate.
// Q's input count includes both, so its cache fraction uses the full prompt.
func acpResponseUsage(usage *acp.Usage) client.Usage {
	if usage == nil {
		return client.Usage{}
	}
	read, write := 0, 0
	if usage.CachedReadTokens != nil {
		read = max(0, *usage.CachedReadTokens)
	}
	if usage.CachedWriteTokens != nil {
		write = max(0, *usage.CachedWriteTokens)
	}
	result := client.Usage{
		PromptTokens:     max(0, usage.InputTokens) + read + write,
		CompletionTokens: max(0, usage.OutputTokens), TotalTokens: max(0, usage.TotalTokens),
	}
	if usage.CachedReadTokens != nil {
		result.PromptDetails = &client.TokenDetails{CachedTokens: read, CacheWriteTokens: write}
	}
	return result
}
