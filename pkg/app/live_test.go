package app_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/q/v2/pkg/app"
	"github.com/snowmerak/q/v2/pkg/app/config"
	"github.com/snowmerak/q/v2/pkg/domain"
	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
	"github.com/snowmerak/q/v2/pkg/llm/gateway"
)

func TestLiveProviders(t *testing.T) {
	if os.Getenv("Q_LIVE_TEST") != "1" {
		t.Skip("set Q_LIVE_TEST=1 to run live provider session tests")
	}

	t.Run("CodexAppServer", func(t *testing.T) {
		if _, err := exec.LookPath("codex"); err != nil {
			t.Skipf("codex CLI not found in PATH: %v", err)
		}
		ephemeral := true
		minimal := true
		cfg := config.Default()
		cfg.LLM.Providers = []gateway.ProviderConfig{{
			ID:      "codex",
			Type:    "codex",
			Enabled: true,
			Codex: gateway.CodexConfig{
				Minimal:        &minimal,
				Sandbox:        "read-only",
				ApprovalPolicy: "never",
				Ephemeral:      &ephemeral,
			},
		}}
		runLiveSession(t, cfg, func(models []llmprovider.Model) (string, string) {
			if override := strings.TrimSpace(os.Getenv("Q_CODEX_MODEL")); override != "" {
				return findModelExact(models, override, "codex/")
			}
			for _, m := range models {
				if strings.HasPrefix(m.ID, "codex/") {
					return m.ID, ""
				}
			}
			return "", "codex model/list returned no models"
		})
	})

	t.Run("OpenAI", func(t *testing.T) {
		if strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) == "" {
			t.Skip("OPENAI_API_KEY is not set")
		}
		cfg := config.Default()
		cfg.LLM.Providers = []gateway.ProviderConfig{{
			ID:        "openai",
			Type:      "openai-compatible",
			Enabled:   true,
			BaseURL:   "https://api.openai.com/v1",
			APIKeyEnv: "OPENAI_API_KEY",
		}}
		runLiveSession(t, cfg, func(models []llmprovider.Model) (string, string) {
			if override := strings.TrimSpace(os.Getenv("Q_OPENAI_MODEL")); override != "" {
				return findModelExact(models, override, "openai/")
			}
			preferred := []string{"openai/gpt-4.1-mini", "openai/gpt-4o-mini", "openai/gpt-5-mini", "openai/gpt-5-nano"}
			if hit := findPreferredModel(models, preferred); hit != "" {
				return hit, ""
			}
			for _, m := range models {
				low := strings.ToLower(m.ID)
				if strings.Contains(low, "gpt-4") && !strings.Contains(low, "audio") && !strings.Contains(low, "realtime") && !strings.Contains(low, "transcribe") && !strings.Contains(low, "tts") {
					return m.ID, ""
				}
			}
			return "", "no suitable OpenAI chat model discovered"
		})
	})

	t.Run("OpenRouter", func(t *testing.T) {
		if strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")) == "" {
			t.Skip("OPENROUTER_API_KEY is not set")
		}
		cfg := config.Default()
		cfg.LLM.Providers = []gateway.ProviderConfig{{
			ID:        "openrouter",
			Type:      "openrouter",
			Enabled:   true,
			APIKeyEnv: "OPENROUTER_API_KEY",
		}}
		runLiveSession(t, cfg, func(models []llmprovider.Model) (string, string) {
			if override := strings.TrimSpace(os.Getenv("Q_OPENROUTER_MODEL")); override != "" {
				return findModelExact(models, override, "openrouter/")
			}
			preferred := []string{
				"openrouter/openai/gpt-4.1-mini",
				"openrouter/openai/gpt-4o-mini",
				"openrouter/google/gemini-2.5-flash",
				"openrouter/deepseek/deepseek-chat",
			}
			if hit := findPreferredModel(models, preferred); hit != "" {
				return hit, ""
			}
			if len(models) > 0 {
				return models[0].ID, ""
			}
			return "", "no OpenRouter models discovered"
		})
	})

	t.Run("DeepInfraDeepSeekV41Flash1017Only", func(t *testing.T) {
		if strings.TrimSpace(os.Getenv("DEEPINFRA_API_KEY")) == "" {
			t.Skip("DEEPINFRA_API_KEY is not set")
		}
		cfg := config.Default()
		cfg.LLM.Providers = []gateway.ProviderConfig{{
			ID:        "deepinfra",
			Type:      "openai-compatible",
			Enabled:   true,
			BaseURL:   "https://api.deepinfra.com/v1/openai",
			APIKeyEnv: "DEEPINFRA_API_KEY",
		}}
		runLiveSession(t, cfg, func(models []llmprovider.Model) (string, string) {
			var deepseekIDs []string
			for _, m := range models {
				low := strings.ToLower(m.ID)
				if strings.Contains(low, "deepseek") {
					deepseekIDs = append(deepseekIDs, m.ID)
				}
				if strings.Contains(low, "deepseek") && strings.Contains(low, "1017") && strings.Contains(low, "flash") {
					return m.ID, ""
				}
			}
			return "", "DeepSeek v4.1 Flash 1017 not listed on DeepInfra (listed deepseek models: " + strings.Join(deepseekIDs, ", ") + ")"
		})
	})

	t.Run("GoogleAIStudioHydrogen", func(t *testing.T) {
		if strings.TrimSpace(os.Getenv("GEMINI_API_KEY")) == "" {
			t.Skip("GEMINI_API_KEY is not set")
		}
		cfg := config.Default()
		cfg.LLM.Providers = []gateway.ProviderConfig{{
			ID:        "google",
			Type:      "openai-compatible",
			Kind:      "generic",
			Enabled:   true,
			BaseURL:   "https://generativelanguage.googleapis.com/v1beta/openai",
			APIKeyEnv: "GEMINI_API_KEY",
		}}
		runLiveSession(t, cfg, func(models []llmprovider.Model) (string, string) {
			if override := strings.TrimSpace(os.Getenv("Q_GEMINI_MODEL")); override != "" {
				return findModelExact(models, override, "google/")
			}
			var ids []string
			for _, m := range models {
				ids = append(ids, m.ID)
				if strings.Contains(strings.ToLower(m.ID), "hydrogen") {
					return m.ID, ""
				}
			}
			return "", "Gemini Hydrogen not listed on Google AI Studio OpenAI endpoint (discovered: " + strings.Join(ids, ", ") + ")"
		})
	})
}

func runLiveSession(t *testing.T, cfg config.Config, pickModel func([]llmprovider.Model) (modelID string, skipReason string)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	if err := config.Save(filepath.Join(dataDir, config.FileName), cfg); err != nil {
		t.Fatal(err)
	}

	a, err := app.Open(ctx, app.Options{DataDir: dataDir, ListenOverride: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("app.Open: %v", err)
	}
	defer func() { _ = a.Close(context.Background()) }()

	gw := a.Gateway()
	if gw == nil {
		t.Fatal("gateway failed to initialize; check provider config or environment variables")
	}
	models, err := gw.Models(ctx)
	if err != nil {
		t.Fatalf("gw.Models: %v", err)
	}
	chosen, skipReason := pickModel(models)
	if skipReason != "" {
		t.Skip(skipReason)
	}
	t.Logf("selected model: %s (discovered=%d)", chosen, len(models))

	srv := httptest.NewServer(a.Handler())
	defer srv.Close()

	postJSON := func(path string, payload any, out any) int {
		b, _ := json.Marshal(payload)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if out != nil && len(raw) > 0 {
			_ = json.Unmarshal(raw, out)
		}
		if resp.StatusCode >= 400 {
			t.Fatalf("POST %s -> %d: %s", path, resp.StatusCode, string(raw))
		}
		return resp.StatusCode
	}

	var persona domain.Persona
	postJSON("/api/v1/personas", map[string]any{"name": "Tester", "is_default": true}, &persona)

	var char domain.Character
	postJSON("/api/v1/characters", map[string]any{
		"name":        "Mina",
		"personality": "Friendly concise assistant who replies in one brief chat bubble.",
		"model_prefs": map[string]any{"chat_model": chosen},
	}, &char)

	var detail struct {
		Room domain.Room `json:"room"`
	}
	postJSON("/api/v1/rooms", map[string]any{
		"title":         "Live Session",
		"mode":          "direct",
		"persona_id":    persona.ID,
		"character_ids": []string{char.ID},
	}, &detail)

	// Attach SSE stream
	sseReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/rooms/"+detail.Room.ID+"/stream", nil)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("open SSE: %v", err)
	}
	defer sseResp.Body.Close()

	type sseMsg struct {
		Event string
		Data  string
	}
	evCh := make(chan sseMsg, 64)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(sseResp.Body)
		sc.Buffer(make([]byte, 8192), 1<<20)
		var curEvent string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				curEvent = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				evCh <- sseMsg{Event: curEvent, Data: strings.TrimPrefix(line, "data: ")}
			}
		}
	}()

	start := time.Now()
	postJSON("/api/v1/rooms/"+detail.Room.ID+"/turns", map[string]any{
		"request_id": "live-1",
		"text":       "Say hello in one short sentence and mention the word lantern.",
	}, nil)

	deltas := 0
	var completionData string
	done := false
	for !done {
		select {
		case ev := <-evCh:
			switch ev.Event {
			case "bubble.delta":
				deltas++
			case "turn.completed":
				completionData = ev.Data
				done = true
			case "turn.failed":
				t.Fatalf("live turn failed for %s: %s", chosen, ev.Data)
			}
		case <-ctx.Done():
			t.Fatalf("live turn timed out for %s", chosen)
		}
	}
	elapsed := time.Since(start)
	cancel()
	wg.Wait()

	msgs, err := a.RDB().Messages().List(context.Background(), detail.Room.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) < 2 {
		t.Fatalf("expected >= 2 committed messages, got %d", len(msgs))
	}
	reply := msgs[len(msgs)-1].Content
	t.Logf("PASS model=%s latency=%s deltas=%d reply_len=%d completion=%s reply_preview=%q", chosen, elapsed.Round(time.Millisecond), deltas, len(reply), completionData, truncateLog(reply, 120))
}

func findPreferredModel(models []llmprovider.Model, preferred []string) string {
	for _, want := range preferred {
		for _, m := range models {
			if m.ID == want {
				return m.ID
			}
		}
	}
	return ""
}

func findModelExact(models []llmprovider.Model, target, prefix string) (string, string) {
	if !strings.HasPrefix(target, prefix) {
		target = prefix + target
	}
	for _, m := range models {
		if m.ID == target {
			return m.ID, ""
		}
	}
	return "", "requested model " + target + " was not discovered"
}

func truncateLog(s string, maxLen int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
