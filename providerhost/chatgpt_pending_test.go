package providerhost

import (
	"context"
	"github.com/snowmerak/llm-provider/gateway"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPendingChatGPTConsentKeepsGatewayChild(t *testing.T) {
	for _, pending := range []bool{true, false} {
		t.Run(map[bool]string{true: "pending", false: "finished"}[pending], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/providers/plan/chatgpt" || r.Header.Get("Authorization") != "Bearer child-key" {
					t.Errorf("incorrect child status request: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				if pending {
					_, _ = w.Write([]byte(`{"pending":true}`))
				} else {
					_, _ = w.Write([]byte(`{"pending":false}`))
				}
			}))
			defer server.Close()
			manager := &Manager{config: gateway.Config{Providers: []gateway.ProviderConfig{{ID: "plan", Type: "chatgpt", Enabled: true}}}, supervisor: &Supervisor{current: &generation{endpoint: server.URL + "/v1", apiKey: "child-key"}}}
			err := manager.checkPendingChatGPTLogin(context.Background())
			if (err != nil) != pending {
				t.Fatalf("pending=%v, error=%v", pending, err)
			}
		})
	}
}
