package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/diagnostics"
	"github.com/snowmerak/q/subagent"
)

type correlatedPlanningClient struct {
	*planningClient
	mu  sync.Mutex
	ids []string
}

func (c *correlatedPlanningClient) Chat(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	c.mu.Lock()
	c.ids = append(c.ids, diagnostics.RequestID(ctx))
	c.mu.Unlock()
	return c.planningClient.Chat(ctx, request)
}

func TestNestedSubagentsShareRequestCorrelation(t *testing.T) {
	parent, call := recoveryParent(t, "workspace/parent", "parent task")
	message := func(name, args string) client.Message {
		return client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(name, args)}}
	}
	d, configured, _ := recoveryDispatcher(t, []subagent.AgentDefinition{recoveryDefinition("workspace/parent", "workspace/child"), recoveryDefinition("workspace/child")},
		message(subagent.TaskStartToolName, `{"objective":"parent task"}`),
		message(subagent.DelegateToolName, `{"subagent_name":"workspace/child","prompt":"child task"}`),
		message(subagent.TaskStartToolName, `{"objective":"child task"}`),
		message(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"child done"}`),
		message(subagent.TaskCompleteToolName, `{"outcome":"succeeded","summary":"parent done"}`),
	)
	wrapped := &correlatedPlanningClient{planningClient: configured}
	d.client = wrapped
	var logs bytes.Buffer
	ctx := diagnostics.WithRequestID(diagnostics.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&logs, nil))), "")
	result, err := d.dispatch(ctx, "", nil, &parent, "", call, delegateInput{SubagentName: "workspace/parent", Prompt: "parent task"})
	if err != nil || result.IsError {
		t.Fatalf("nested delegation: %+v %v", result, err)
	}
	requestID := diagnostics.RequestID(ctx)
	if len(wrapped.ids) < 5 {
		t.Fatalf("missing model calls: %v", wrapped.ids)
	}
	for _, id := range wrapped.ids {
		if id != requestID {
			t.Fatalf("model lost request ID: %q", id)
		}
	}
	store := parent
	for range 2 {
		bookmarks, err := store.LoadDelegations()
		if err != nil || len(bookmarks) != 1 {
			t.Fatalf("nested bookmarks: %+v %v", bookmarks, err)
		}
		store, err = store.ChildStore(bookmarks[0].InvocationID)
		if err != nil {
			t.Fatal(err)
		}
		state, err := store.LoadDelegationState()
		if err != nil || state.RequestID != requestID {
			t.Fatalf("nested state: %+v %v", state, err)
		}
	}
	started := 0
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record["request_id"] != requestID {
			t.Fatalf("subagent log lost request ID: %s", line)
		}
		if record["msg"] == "studio.subagent.started" {
			started++
		}
	}
	if started != 2 {
		t.Fatalf("subagent start logs=%d: %s", started, logs.String())
	}
}

func TestLiveSessionRefreshesCorrelationForEveryTurn(t *testing.T) {
	configured := &correlatedPlanningClient{planningClient: &planningClient{responses: []client.Message{{Role: client.RoleAssistant, Content: "first"}, {Role: client.RoleAssistant, Content: "second"}}}}
	session, _ := testLiveSession(t, configured)
	ids := []string{}
	for _, prompt := range []string{"first", "second"} {
		ctx := diagnostics.WithRequestID(t.Context(), "")
		ids = append(ids, diagnostics.RequestID(ctx))
		if _, err := session.execute(ctx, prompt, false, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(configured.ids) != 2 || configured.ids[0] != ids[0] || configured.ids[1] != ids[1] || ids[0] == ids[1] {
		t.Fatalf("turn IDs=%v expected=%v", configured.ids, ids)
	}
}
