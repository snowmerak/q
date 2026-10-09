package subagent

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
)

type batchTestRuntime struct {
	secondDone chan struct{}
	calls      atomic.Int32
	committed  atomic.Bool
}

func (*batchTestRuntime) Tools() []client.Tool {
	return []client.Tool{{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "read_file"}}}
}
func (r *batchTestRuntime) Call(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
	r.calls.Add(1)
	if !r.committed.Load() {
		return client.ToolResult{}, errors.New("read overtook memory checkpoint")
	}
	if call.ID == "d" {
		close(r.secondDone)
	} else {
		select {
		case <-r.secondDone:
		case <-ctx.Done():
			return client.ToolResult{}, ctx.Err()
		}
	}
	return client.ToolResult{Content: call.ID}, nil
}

func TestGeneralRunnerRecoversWholeInterruptedSegmentBeforeNextBarrier(t *testing.T) {
	for _, saved := range []int{0, 1} {
		t.Run(map[int]string{0: "no_results", 1: "first_result_saved"}[saved], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			call := func(id, name, arguments string) client.ToolCall {
				return client.ToolCall{ID: id, Function: client.FunctionCall{Name: name, Arguments: arguments}}
			}
			calls := []client.ToolCall{
				call("a", "read_file", `{}`), call("b", "read_file", `{}`),
				call("memory", memory.RecordFactTool, `{"fact":"interrupted batch reviewed"}`),
				call("c", "read_file", `{}`), call("d", "read_file", `{}`),
			}
			messages := []client.Message{{Role: client.RoleSystem, Content: "Work"}, {Role: client.RoleUser, Content: "Inspect"}, {Role: client.RoleAssistant, ToolCalls: calls}}
			if saved > 0 {
				messages = append(messages, client.ToolResultMessage(calls[0], client.ToolResult{Content: "a saved"}))
			}
			state := GeneralRunState{Transcript: messages, Context: messages, Round: 1, Started: true}
			runtime := &batchTestRuntime{secondDone: make(chan struct{})}
			runner := &GeneralRunner{Tools: runtime, Resume: &state, Definition: AgentDefinition{Info: DelegateInfo{Name: "worker"}}}
			history := NewContextCompactor(Spec{}, messages, runtime.Tools(), 2)
			started := true
			checkpoint := func() error {
				last := state.Transcript[len(state.Transcript)-1]
				if last.Role == client.RoleTool && last.Name == memory.RecordFactTool {
					runtime.committed.Store(true)
				}
				return nil
			}
			_, done, err := runner.completeGeneralCalls(ctx, &state, history, runtime.Tools(), "task", nil, &started, checkpoint)
			if err != nil || done || runtime.calls.Load() != 2 {
				t.Fatalf("done=%v err=%v dispatched=%d", done, err, runtime.calls.Load())
			}
			var ids []string
			for _, message := range state.Transcript[3:] {
				ids = append(ids, message.ToolCallID)
				if (message.ToolCallID == "a" && saved == 0) || message.ToolCallID == "b" {
					if message.Content != `Tool error: {"status":"unknown","detail":"tool execution outcome could not be confirmed after session restart"}` {
						t.Errorf("interrupted call was replayed: %#v", message)
					}
				}
			}
			if !reflect.DeepEqual(ids, []string{"a", "b", "memory", "c", "d"}) {
				t.Fatalf("checkpoint order = %v", ids)
			}
		})
	}
}
