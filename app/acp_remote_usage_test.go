package app

import (
	"testing"

	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

func TestACPUsageNormalizesCacheDenominatorAndPreservesUnknown(t *testing.T) {
	for _, test := range []struct {
		name   string
		usage  *acp.Usage
		input  int
		cached *int
	}{
		{name: "absent"},
		{name: "cached", usage: &acp.Usage{InputTokens: 20, CachedReadTokens: new(60), CachedWriteTokens: new(20), OutputTokens: 10, TotalTokens: 110}, input: 100, cached: new(60)},
		{name: "zero cache", usage: &acp.Usage{InputTokens: 80, CachedReadTokens: new(0), CachedWriteTokens: new(20), OutputTokens: 10}, input: 100, cached: new(0)},
		{name: "unknown cache", usage: &acp.Usage{InputTokens: 100, OutputTokens: 10}, input: 100},
		{name: "only writes", usage: &acp.Usage{InputTokens: 80, CachedWriteTokens: new(20), OutputTokens: 10}, input: 100},
	} {
		t.Run(test.name, func(t *testing.T) {
			usage := acpResponseUsage(test.usage)
			if usage.PromptTokens != test.input {
				t.Fatalf("input = %d, want %d", usage.PromptTokens, test.input)
			}
			counts := responseTokenUsage(usage)
			if test.usage == nil {
				if counts != nil {
					t.Fatalf("invented usage: %+v", counts)
				}
				return
			}
			if counts == nil || (counts.CachedTokens == nil) != (test.cached == nil) {
				t.Fatalf("cache availability = %+v", counts)
			}
			if test.cached != nil && *counts.CachedTokens != *test.cached {
				t.Fatalf("cache = %d, want %d", *counts.CachedTokens, *test.cached)
			}
		})
	}
}
