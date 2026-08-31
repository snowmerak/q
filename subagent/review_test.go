package subagent

import (
	"context"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

func TestReviewRunnerDelegatesScoutAndUsesCapturedExternalSearch(t *testing.T) {
	advisorClient := &fakeScoutClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(DelegateScoutToolName, `{
			"objective":"Inspect cache invalidation around Set",
			"candidate_files":["cache.go"],
			"completion_criteria":["Identify whether cached values can survive Set"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(ExternalSearchToolName, `{
			"query":"current cache invalidation correctness guidance",
			"completion_criteria":["Find authoritative guidance relevant to stale cached values"]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(SubmitReviewReportToolName, `{
			"summary":"The new branch can return a stale value.",
			"verdict":"request_changes",
			"findings":[{
				"severity":"high",
				"title":"Cached value is not invalidated",
				"explanation":"The changed setter leaves the cached result reachable.",
				"locations":[{"path":"cache.go","line":42,"symbol":"Set"}],
				"recommendation":"Invalidate the cached result in Set and cover the transition with a test."
			}],
			"verification_gaps":["The invalidation path has no regression test"]
		}`)}},
	}}
	scoutClient := &fakeScoutClient{responses: []client.Message{{
		Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(ScoutCompleteToolName, `{
			"outcome":"succeeded",
			"summary":"Set leaves the previous cached value reachable",
			"findings":[{
				"path":"cache.go","symbol":"Set",
				"summary":"The setter does not invalidate the cache",
				"evidence":["Set replaces the source value without clearing cachedResult"]
			}]
		}`)},
	}}}
	baseTools := &fakeScoutTools{available: []client.Tool{
		scoutFunctionTool("edit_file"),
		scoutFunctionTool("run_command"),
		scoutFunctionTool("read_file"),
		scoutFunctionTool("lsp_diagnostics"),
		scoutFunctionTool("search_skills"),
	}}
	var searchInput ExternalSearchInput
	tools, err := NewInvocationRuntime(baseTools, testInvocationCapture, Invocation{
		Tool: ExternalSearchTool(),
		Source: InvocationSource{
			Protocol: "acp", Name: "search-agent", Kind: "agent-result",
			MediaType: "application/vnd.q.agent-result+json",
		},
		Handler: func(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
			parsed, err := ParseExternalSearchInput(call.Function.Arguments)
			if err != nil {
				return client.ToolResult{}, err
			}
			searchInput = parsed
			return jsonToolResult(ExternalSearchResult{
				Agent: "search-agent", Summary: "Caches must invalidate stale entries after mutation",
				Sources: []string{"https://example.com/cache-guidance"},
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := (ReviewRunner{
		Client: advisorClient, Tools: tools, Capture: testInvocationCapture,
		Scout: ScoutRunner{
			Client: scoutClient, Tools: baseTools,
			Spec: Spec{Role: config.AgentRoleScout, Model: "scout-model"},
		},
		Spec: Spec{Role: config.AgentRoleAdvisor, Model: "review-model"},
	}).Run(t.Context(), ReviewRequest{
		ID: "review-1", Objective: "Review cache invalidation",
		Files: []ReviewFile{{Path: "cache.go", Status: " M", Sections: []ReviewPatch{{
			Title: "UNSTAGED", Patch: "@@ -42 +42 @@\n-old\n+new\n",
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != "request_changes" || len(report.Findings) != 1 || report.Findings[0].Locations[0].Line != 42 {
		t.Fatalf("report = %#v", report)
	}
	if len(advisorClient.requests) != 3 || len(scoutClient.requests) != 1 {
		t.Fatalf("Advisor requests = %d, Scout requests = %d", len(advisorClient.requests), len(scoutClient.requests))
	}
	if searchInput.Query != "current cache invalidation correctness guidance" {
		t.Fatalf("external search input = %#v", searchInput)
	}
	request := advisorClient.requests[0]
	for _, expected := range []string{DelegateScoutToolName, ExternalSearchToolName, "search_skills", SubmitReviewReportToolName} {
		if !hasTool(request.Tools, expected) {
			t.Fatalf("review omitted investigation tool %q: %#v", expected, request.Tools)
		}
	}
	for _, forbidden := range []string{"edit_file", "write_file", "run_command", "read_file", "lsp_diagnostics"} {
		if hasTool(request.Tools, forbidden) {
			t.Fatalf("Advisor received direct repository or mutation tool %q", forbidden)
		}
	}
	for index, expected := range []string{
		"Set leaves the previous cached value reachable",
		"Caches must invalidate stale entries after mutation",
	} {
		messages := advisorClient.requests[index+1].Messages
		result := messages[len(messages)-1].Content
		if !strings.Contains(result, expected) || !strings.Contains(result, "loom_ref") {
			t.Fatalf("captured evidence %d = %q", index, result)
		}
	}
	prompt := request.Messages[0].Content + "\n" + request.Messages[1].Content
	for _, expected := range []string{
		"read-only Advisor", "delegate_scout", "external_search", "Do not modify the workspace",
		"cache.go", "@@ -42 +42 @@", "Review cache invalidation",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("review prompt omitted %q: %s", expected, prompt)
		}
	}
}

func TestReviewReportValidationAndRendering(t *testing.T) {
	if _, err := parseReviewReport(`{"summary":"bad","verdict":"request_changes","findings":[]}`); err == nil {
		t.Fatal("request_changes without findings was accepted")
	}
	if _, err := parseReviewReport(`{
		"summary":"bad","verdict":"approve","findings":[{
			"severity":"high","title":"bug","explanation":"breaks",
			"locations":[{"path":"main.go","line":2}],"recommendation":"fix"
		}]
	}`); err == nil {
		t.Fatal("approve with a high-severity finding was accepted")
	}
	report, err := parseReviewReport(`{
		"summary":"No correctness issue was found in the supplied changes.",
		"verdict":"APPROVE",
		"findings":[],
		"verification_gaps":["The platform-specific path was not exercised"]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	rendered := RenderReviewReport(report)
	for _, expected := range []string{"No actionable findings", "Summary:", "Verdict: approve", "Verification gaps"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered report omitted %q: %s", expected, rendered)
		}
	}
}
