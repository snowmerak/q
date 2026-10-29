package studio

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/llm-provider/providers/chatgpt"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/workspace"
)

type chatGPTStudioProbeWriter struct {
	http.ResponseWriter
	body bytes.Buffer
}

func (w *chatGPTStudioProbeWriter) Write(body []byte) (int, error) {
	_, _ = w.body.Write(body)
	return w.ResponseWriter.Write(body)
}
func (w *chatGPTStudioProbeWriter) Flush() { w.ResponseWriter.(http.Flusher).Flush() }

func TestLiveStudioChatGPTPersonalChat(t *testing.T) {
	if os.Getenv("Q_TEST_CHATGPT_STUDIO") != "1" {
		t.Skip("set Q_TEST_CHATGPT_STUDIO=1 to use the existing Q ChatGPT login")
	}
	actualStore, err := config.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	settings, err := actualStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	p, err := chatgpt.New(chatgpt.Options{Directory: filepath.Join(actualStore.Dir, "chatgpt"), AppID: "q", AppName: "Q"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	status, err := p.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	connected := false
	for _, profile := range status.Profiles {
		connected = connected || profile.ID == status.Active && profile.Connected && profile.PlanEnabled
	}
	if !connected {
		t.Skip("no connected Q ChatGPT plan account; no login attempted")
	}
	models, err := p.ListModels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	model := ""
	for _, candidate := range models {
		if strings.Contains(candidate.ID, "gpt-6.1") && strings.Contains(candidate.ID, "sol") {
			model = candidate.ID
			break
		}
	}
	if model == "" {
		t.Skip("GPT-6.1 Sol is unavailable")
	}
	g, err := gateway.NewContext(t.Context(), providerhost.LocalConfig(gateway.Config{Providers: []gateway.ProviderConfig{{ID: "plan", Prefix: "plan", Type: "chatgpt", Enabled: true, Models: []string{model}}}}, actualStore.Dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	fixture := newStudioIntegrationConfigured(t, func(w http.ResponseWriter, r *http.Request) {
		probe := &chatGPTStudioProbeWriter{ResponseWriter: w}
		g.Handler().ServeHTTP(probe, r)
		for _, line := range bytes.Split(probe.body.Bytes(), []byte("\n")) {
			var event struct {
				Type string
				Item struct {
					Type, Phase string
					Content     []struct{ Type, Text string }
				}
				Response struct {
					Status string
					Output []struct{ Type, Phase string }
				}
			}
			if json.Unmarshal(bytes.TrimPrefix(line, []byte("data: ")), &event) != nil {
				continue
			}
			if event.Type == "response.output_item.done" {
				t.Logf("item done type=%s phase=%s parts=%d", event.Item.Type, event.Item.Phase, len(event.Item.Content))
			}
			if event.Type == "response.completed" {
				t.Logf("completed status=%s output_items=%d", event.Response.Status, len(event.Response.Output))
			}
		}
	}, func(value *config.Config) {
		value.Provider.Model = "plan/" + model
		value.Provider.SystemPrompt = settings.Provider.SystemPrompt
		value.Provider.ReasoningEffort = settings.Provider.ReasoningEffort
		value.ModelAPIModes = map[string]string{"plan/" + model: "responses", "default/plan/" + model: "responses"}
	})
	server := httptest.NewServer(fixture.handler)
	t.Cleanup(server.Close)
	chat := createTestChat(t, fixture.handler)
	fixture.root = chat.WorkspaceRoot
	fixture.session = workspace.Store{Root: chat.WorkspaceRoot, SessionID: chat.Session.SessionID}
	// Recreate the reported user-only failed history in this disposable chat.
	saved, err := fixture.session.Load()
	if err != nil {
		t.Fatal(err)
	}
	saved.Transcript = []client.Message{{Role: client.RoleUser, Content: "안녕"}, {Role: client.RoleUser, Content: "?"}, {Role: client.RoleUser, Content: "?"}}
	if err := fixture.session.Save(saved); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"?", "안녕"} {
		previousAnswers := 0
		for _, message := range saved.Transcript {
			if message.Role == client.RoleAssistant && strings.TrimSpace(message.Content) != "" {
				previousAnswers++
			}
		}
		run := fixture.start(t, prompt)
		endpoint := server.URL + "/api/v1/sessions/" + chat.Session.SessionID + "/runs/" + run.ID + "/events?workspace_root=" + url.QueryEscape(chat.WorkspaceRoot)
		page := waitStudioPage(t, endpoint, func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
		if page.Run.Status != "completed" {
			t.Fatalf("personal chat failed: status=%s error=%s", page.Run.Status, page.Run.Error)
		}
		saved, err = fixture.session.Load()
		if err != nil {
			t.Fatal(err)
		}
		answers := 0
		for _, message := range saved.Transcript {
			if message.Role == client.RoleAssistant && strings.TrimSpace(message.Content) != "" {
				answers++
			}
		}
		if answers <= previousAnswers {
			t.Fatal("completed chat has no new persisted assistant answer")
		}
	}
}
