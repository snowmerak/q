package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

func TestDelegatedParallelToolsAreCoalescedAndMatched(t *testing.T) {
	state := newTurnState(context.Background(), nil, "thread_parallel", "model", nil, true)
	state.setTurnID("turn_parallel")
	for index, id := range []string{"call_a", "call_b"} {
		ok := state.addDelegatedTool(llmprovider.ToolCall{
			ID: id, Type: llmprovider.ToolTypeFunction,
			Function: llmprovider.FunctionCall{Name: "lookup", Arguments: `{}`},
		}, json.RawMessage([]byte{byte('1' + index)}))
		if !ok {
			t.Fatalf("tool %s was not delegated", id)
		}
	}
	stream := &codexStream{state: state, ctx: context.Background()}
	for index, id := range []string{"call_a", "call_b"} {
		chunk, err := stream.Recv()
		if err != nil || chunk.Choices[0].Delta.ToolCalls[0].ID != id ||
			chunk.Choices[0].Delta.ToolCalls[0].Index != index {
			t.Fatalf("chunk %d = %#v, err = %v", index, chunk, err)
		}
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("pause error = %v", err)
	}

	pending, results, err := state.matchDelegatedToolResults([]llmprovider.Message{
		{Role: llmprovider.RoleTool, ToolCallID: "call_b", Content: "B"},
		{Role: llmprovider.RoleTool, ToolCallID: "call_a", Content: "A"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 || len(results) != 2 || results[0].Content != "A" || results[1].Content != "B" {
		t.Fatalf("pending = %#v, results = %#v", pending, results)
	}
	if len(state.delegatedTools()) != 2 {
		t.Fatal("matching results removed callbacks before their responses were written")
	}
	for _, item := range pending {
		if !state.acknowledgeDelegatedTool(item) {
			t.Fatalf("tool %s was not acknowledged", item.call.ID)
		}
	}
	if state.isAwaitingTools() {
		t.Fatal("state remained paused after every callback was acknowledged")
	}
}

func TestDelegatedSequentialToolsAreExposedOneAtATime(t *testing.T) {
	state := newTurnState(context.Background(), nil, "thread_sequential", "model", nil, false)
	state.setTurnID("turn_sequential")
	for index, id := range []string{"call_a", "call_b"} {
		if !state.addDelegatedTool(llmprovider.ToolCall{
			ID: id, Type: llmprovider.ToolTypeFunction,
			Function: llmprovider.FunctionCall{Name: "lookup", Arguments: `{}`},
		}, json.RawMessage([]byte{byte('1' + index)})) {
			t.Fatalf("tool %s was not delegated", id)
		}
	}
	stream := &codexStream{state: state, ctx: context.Background()}
	first, err := stream.Recv()
	if err != nil || first.Choices[0].Delta.ToolCalls[0].ID != "call_a" ||
		first.Choices[0].Delta.ToolCalls[0].Index != 0 {
		t.Fatalf("first chunk = %#v, err = %v", first, err)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("first pause error = %v", err)
	}

	pending, results, err := state.matchDelegatedToolResults([]llmprovider.Message{{
		Role: llmprovider.RoleTool, ToolCallID: "call_a", Content: "A",
	}})
	if err != nil || len(pending) != 1 || len(results) != 1 {
		t.Fatalf("first match pending = %#v, results = %#v, err = %v", pending, results, err)
	}
	if len(state.delegatedTools()) != 2 {
		t.Fatal("matching the first result removed it before transport acknowledgement")
	}
	if !state.acknowledgeDelegatedTool(pending[0]) {
		t.Fatal("first callback was not acknowledged")
	}

	second, err := stream.Recv()
	if err != nil || second.Choices[0].Delta.ToolCalls[0].ID != "call_b" ||
		second.Choices[0].Delta.ToolCalls[0].Index != 0 {
		t.Fatalf("second chunk = %#v, err = %v", second, err)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("second pause error = %v", err)
	}
}
