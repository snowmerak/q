package studio

import (
	"context"
	"encoding/json"
	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
	"net/http/httptest"
	"strings"
	"testing"
)

type chatGPTRuntime struct {
	calls        int
	method, path string
	body         json.RawMessage
}

func (*chatGPTRuntime) ApplyGateway(context.Context, gateway.Config) error { return nil }
func (r *chatGPTRuntime) GatewayRequest(_ context.Context, method, path string, body json.RawMessage) (json.RawMessage, error) {
	r.calls++
	r.method, r.path, r.body = method, path, body
	return json.RawMessage(`{"active_profile":"account","profiles":[],"pending":false}`), nil
}
func TestChatGPTDelegatesToRunningGateway(t *testing.T) {
	runtime := &chatGPTRuntime{}
	service := newSettingsService(config.Store{Dir: t.TempDir()}, runtime)
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/v1/settings/gateway/providers/plan/chatgpt/select", strings.NewReader(`{"profile":"account"}`))
	r.RemoteAddr = "127.0.0.1:4321"
	r.Header.Set("Content-Type", "application/json")
	r.SetPathValue("provider", "plan")
	r.SetPathValue("action", "select")
	w := httptest.NewRecorder()
	service.serveChatGPT(w, r)
	if w.Code != 200 || runtime.calls != 1 || runtime.path != "/v1/providers/plan/chatgpt/select" || !strings.Contains(string(runtime.body), `"profile":"account"`) {
		t.Fatalf("incorrect delegation: %d %+v", w.Code, runtime)
	}
}
func TestChatGPTStudioRejectsNonlocalAndCrossOrigin(t *testing.T) {
	for _, tc := range []struct{ remote, host, origin string }{{"192.0.2.2:4321", "localhost:8080", ""}, {"127.0.0.1:4321", "evil.example:8080", "http://evil.example:8080"}, {"127.0.0.1:4321", "localhost:8080", "https://evil.example"}} {
		runtime := &chatGPTRuntime{}
		service := newSettingsService(config.Store{Dir: t.TempDir()}, runtime)
		r := httptest.NewRequest("GET", "http://"+tc.host+"/api/v1/settings/gateway/providers/plan/chatgpt", nil)
		r.RemoteAddr = tc.remote
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		service.serveChatGPT(w, r)
		if w.Code != 403 || runtime.calls != 0 {
			t.Fatalf("unsafe request delegated: %d %+v", w.Code, tc)
		}
	}
}
