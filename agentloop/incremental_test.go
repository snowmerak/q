package agentloop_test

import (
	"context"
	"strings"
	"testing"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
)

func TestCoveredCompactionSkipsModelAndTransfersToHost(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "apply", true: "cancel"}[cancelled], func(t *testing.T) {
			policy := memory.Policy{ContextWindow: 16_000, TriggerRatio: .85, TargetRatio: .22, RecentRatio: .07}
			loop := agentloop.NewContext(policy, []client.Message{
				{Role: client.RoleSystem, Content: "role contract"},
				{Role: client.RoleUser, Content: "Implement D only"},
				{Role: client.RoleAssistant, Content: strings.Repeat("completed investigation ", 3000)},
			}, nil)
			for _, input := range []struct{ name, arguments string }{
				{memory.RecordFactTool, `{"fact":"Tests passed","source":"loom://checks"}`},
				{memory.CheckpointTool, `{"expected_revision":1}`},
			} {
				call := client.ToolCall{ID: input.name, Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: input.name, Arguments: input.arguments}}
				loop.Append(client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call}})
				result, handled := loop.CallMemoryTool(call)
				if !handled || result.IsError {
					t.Fatalf("memory tool: %s", result.Content)
				}
				loop.Append(client.ToolResultMessage(call, result))
			}
			// An embedding host can persist the loop's plan without configuring
			// its own compaction policy.
			host := memory.New(memory.Policy{}, loop.Messages())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelled {
				cancel()
			}
			// A nil model client proves this path cannot issue a summary request.
			compaction, err := loop.CompactIfNeeded(ctx, nil, "test", "")
			if cancelled {
				if err == nil || compaction != nil || len(loop.Messages()) != len(host.Messages()) {
					t.Fatal("cancelled direct compaction changed context")
				}
				return
			}
			if err != nil || compaction == nil || compaction.Plan.CoveredMessages == 0 {
				t.Fatalf("compaction=%#v err=%v", compaction, err)
			}
			if err := host.Apply(compaction.Plan, compaction.Summary); err != nil {
				t.Fatal(err)
			}
			if joined(host.Messages()) != joined(loop.Messages()) || !strings.Contains(joined(host.Messages()), "loom://checks") {
				t.Fatal("host and loop diverged after direct compaction")
			}
		})
	}
}
