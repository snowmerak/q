package app

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

func TestACPAgentPromptIntegration(t *testing.T) {
	if strings.TrimSpace(os.Getenv("Q_TEST_ACP_PROMPT")) == "" {
		t.Skip("set Q_TEST_ACP_PROMPT=1 and Q_TEST_ACP_PRESET=codex, claude, or grok to send a real ACP prompt")
	}
	preset := strings.TrimSpace(os.Getenv("Q_TEST_ACP_PRESET"))
	if preset == "" {
		t.Fatal("Q_TEST_ACP_PRESET must be codex, claude, or grok")
	}
	command, err := resolveConfiguredACPAgentCommand(config.AgentConnectionConfig{Preset: preset}, exec.LookPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("ACP command: %s %s", command.name, strings.Join(command.args, " "))
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	remote, err := startACPRemoteClient(ctx, command, t.TempDir(), "", "", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := disposeACPRemote(remote); err != nil {
			t.Errorf("dispose ACP agent: %v", err)
		}
	})
	remote.permissions = acpPermissionReadOnly
	remote.requireCompletedText = true
	t.Logf("Connected to %s; session created", remote.display)

	const marker = "Q_ACP_ROUNDTRIP_OK"
	prompt := "Do not use tools or inspect files. Reply with exactly this text and nothing else: " + marker
	t.Logf("Sending prompt: %s", prompt)
	events := make(chan agentEvent)
	go remote.runPrompt(ctx, prompt, events)
	var streamed strings.Builder
	var final *client.ChatResponse
	var promptErr error
	for event := range events {
		if event.err != nil {
			promptErr = event.err
		}
		if event.streamDelta != nil && event.streamDelta.Kind != chatStreamThinking {
			streamed.WriteString(event.streamDelta.Content)
		}
		if event.response != nil {
			final = event.response
		}
	}
	if promptErr != nil {
		t.Fatal(promptErr)
	}
	if final == nil || len(final.Choices) != 1 {
		t.Fatalf("missing final ACP response: %#v", final)
	}
	text := strings.TrimSpace(final.Choices[0].Message.TextContent())
	if text != marker || strings.TrimSpace(streamed.String()) != marker {
		t.Fatalf("ACP round trip: final=%q streamed=%q; want %q", text, streamed.String(), marker)
	}
	t.Logf("Streamed and final responses both received: %s", text)
}
