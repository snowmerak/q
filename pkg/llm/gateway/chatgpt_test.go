package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatGPTManagementLocalBoundary(t *testing.T) {
	g, err := New(Config{Providers: []ProviderConfig{{ID: "plan", Prefix: "plan", Type: "chatgpt", Enabled: true, ChatGPT: ChatGPTConfig{Directory: t.TempDir()}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	for _, tc := range []struct {
		name, remote, host, origin, site string
		code                             int
	}{
		{"local", "127.0.0.1:4321", "127.0.0.1:8080", "", "", 200},
		{"ipv6", "[::1]:4321", "localhost:8080", "http://localhost:8080", "same-origin", 200},
		{"remote", "192.0.2.1:4321", "127.0.0.1:8080", "", "", 403},
		{"rebind", "127.0.0.1:4321", "evil.example:8080", "http://evil.example:8080", "same-origin", 403},
		{"origin", "127.0.0.1:4321", "127.0.0.1:8080", "https://evil.example", "", 403},
		{"cross-site", "127.0.0.1:4321", "127.0.0.1:8080", "", "cross-site", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://"+tc.host+"/v1/providers/plan/chatgpt", nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			w := httptest.NewRecorder()
			g.Handler().ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
			}
			for _, key := range []string{"access_token", "refresh_token", "client_id", "ext_agent_host_id"} {
				if strings.Contains(w.Body.String(), key) {
					t.Errorf("management exposed %s", key)
				}
			}
		})
	}
	r := httptest.NewRequest("POST", "http://localhost/v1/providers/plan/chatgpt/login", strings.NewReader(`{}`))
	r.RemoteAddr = "127.0.0.1:4321"
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form action accepted: %d", w.Code)
	}
}

func TestChatGPTRejectsAlternateCredentialAndEndpoint(t *testing.T) {
	for _, field := range []string{"api_key", "api_key_env", "base_url"} {
		p := ProviderConfig{ID: "plan", Prefix: "plan", Type: "chatgpt", Enabled: true}
		switch field {
		case "api_key":
			p.APIKey = "secret"
		case "api_key_env":
			p.APIKeyEnv = "SECRET"
		case "base_url":
			p.BaseURL = "https://evil.example"
		}
		if err := p.validate(); err == nil {
			t.Errorf("%s accepted", field)
		}
	}
}
