package app

import (
	"context"
	"sync"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/diagnostics"
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
