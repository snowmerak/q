package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentToolsRegistration(t *testing.T) {
	cfg := &Config{
		APIEndpoint:       "http://localhost:8080/v1",
		Model:             "test-model",
		MaxContextTokens:  4096,
		APITimeoutSeconds: 5,
	}
	sysInfo := &SystemInfo{
		OS:       "windows",
		Shell:    "powershell",
		Username: "testuser",
		PWD:      ".",
	}
	session := &Session{
		Name: "test-session",
	}

	agent := NewAgent(cfg, sysInfo, session)

	// Check builtin tools are registered
	expectedTools := []string{"run_shell_command", "read_file", "edit_file", "insert_file", "erase_file"}
	for _, name := range expectedTools {
		if _, ok := agent.Tools[name]; !ok {
			t.Errorf("Expected tool %s to be registered", name)
		}
	}
}

func TestAgentRunLoop(t *testing.T) {
	// Create a mock OpenAI API server
	mockLLMResponse := ChatResponse{
		Choices: []ChatChoice{
			{
				Message: ChatMessage{
					Role:    "assistant",
					Content: "Hello world!",
				},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockLLMResponse)
	}))
	defer server.Close()

	cfg := &Config{
		APIEndpoint:       server.URL,
		Model:             "test-model",
		MaxContextTokens:  4096,
		APITimeoutSeconds: 5,
	}
	sysInfo := &SystemInfo{
		OS:       "windows",
		Shell:    "powershell",
		Username: "testuser",
		PWD:      ".",
	}
	session := &Session{
		Name: "test-session",
		Messages: []ChatMessage{
			{Role: "user", Content: "Run loop test"},
		},
	}

	agent := NewAgent(cfg, sysInfo, session)
	agent.Run(context.Background())

	if len(session.Messages) < 2 {
		t.Fatalf("Expected messages to have at least 2 entries, got %d", len(session.Messages))
	}
	lastMsg := session.Messages[len(session.Messages)-1]
	if lastMsg.Role != "assistant" || lastMsg.Content != "Hello world!" {
		t.Errorf("Unexpected last message: %+v", lastMsg)
	}
}
