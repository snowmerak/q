package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

func TestACPLibraryInstructionsRetryAndReset(t *testing.T) {
	connection := &fakeACPRemoteConnection{newSessionID: "next"}
	remote := &acpRemoteClient{connection: connection, sessionID: "first", mcpServers: []acp.McpServer{{Stdio: &acp.McpServerStdio{Name: "q-library"}}}}
	var prompts []string
	connection.prompt = func(_ context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
		prompts = append(prompts, request.Prompt[0].Text.Text)
		if len(prompts) == 1 {
			return acp.PromptResponse{}, errors.New("temporary failure")
		}
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}
	if _, _, err := remote.prompt(t.Context(), "first request", nil); err == nil {
		t.Fatal("failed prompt succeeded")
	}
	if _, _, err := remote.prompt(t.Context(), "retry", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := remote.prompt(t.Context(), "second", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompts[0], "Q Library provides") || !strings.Contains(prompts[1], "Q Library provides") || prompts[2] != "second" {
		t.Fatalf("instructions not retried or deduplicated: %v", prompts)
	}
	if _, err := remote.newSession(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := remote.prompt(t.Context(), "new session", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompts[3], "Q Library provides") {
		t.Fatal("new session omitted Library instructions")
	}
}
