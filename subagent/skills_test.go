package subagent

import (
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestRetrievalCatalogRequiresBoundaryChecksForAvailableSources(t *testing.T) {
	tools := []client.Tool{
		scoutFunctionTool("search_skills"),
		scoutFunctionTool("get_skill"),
		scoutFunctionTool("search_archive"),
		scoutFunctionTool("get_archive_record"),
	}
	prompt := withRetrievalCatalog("base prompt", tools)
	for _, required := range []string{
		"Before starting substantive work",
		"Before finalizing substantive work, search again",
		"search_skills",
		"get_skill",
		"search_archive",
		"get_archive_record",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("retrieval guidance does not require %q:\n%s", required, prompt)
		}
	}
}

func TestRetrievalCatalogDoesNotRequireUnavailableSources(t *testing.T) {
	tools := []client.Tool{scoutFunctionTool("search_skills"), scoutFunctionTool("get_skill")}
	prompt := withRetrievalCatalog("base prompt", tools)
	if !strings.Contains(prompt, "search_skills") || strings.Contains(prompt, "search_archive") {
		t.Fatalf("retrieval guidance does not match available tools:\n%s", prompt)
	}
}
