package systemone

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestEvaluateSendsNativeQuestionsAndReadsAnswers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/systemone" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-key" || request.Header.Get("Content-Type") != "application/json" || request.Header.Get("Idempotency-Key") != "ticket-123" {
			t.Errorf("request headers = %#v", request.Header)
		}
		var body struct {
			Model     string                     `json:"model"`
			State     string                     `json:"state"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Model != "jev-latest" || body.State != "A customer was charged twice" || len(body.Questions) != 3 {
			t.Errorf("request body = %#v", body)
		}
		for name, kind := range map[string]string{"team": "choice", "urgency": "score", "duplicate": "noul"} {
			var question struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(body.Questions[name], &question); err != nil || question.Type != kind {
				t.Errorf("question %s = %s, %v", name, body.Questions[name], err)
			}
		}
		writer.Header().Set("X-Request-Id", "req-123")
		writer.Header().Set("X-System-One-Credits", "3")
		_, _ = io.WriteString(writer, `{"model":"jev-1.13.0","answers":{"team":{"type":"choice","choice":"billing","probabilities":{"billing":0.8,"support":0.2},"confidence":0.6,"extension":true},"urgency":{"type":"score","score":1.5,"probabilities":{"0":0.1,"1":0.3,"2":0.6},"confidence":0.3,"legend":{"0":"Normal","1":"Timely","2":"Immediate"}},"duplicate":{"type":"noul","noul":0.8}},"usage":{"input_tokens":120,"output_tokens":0}}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL + "/v1", APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Evaluate(t.Context(), Request{
		Model: "jev-latest", State: "A customer was charged twice",
		Questions: map[string]Question{
			"team":      {Type: QuestionChoice, Instructions: "Choose a team", Criteria: map[string]string{"billing": "Payments", "support": "Technical help"}},
			"urgency":   {Type: QuestionScore, Instructions: "Rate urgency", Criteria: []string{"Normal", "Timely", "Immediate"}},
			"duplicate": {Type: QuestionNoul, Instructions: "Duplicate charge?"},
		},
	}, CallOptions{IdempotencyKey: "ticket-123"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "jev-1.13.0" || result.Answers["team"].Choice != "billing" || result.Answers["urgency"].Score == nil || *result.Answers["urgency"].Score != 1.5 || result.Answers["duplicate"].Noul == nil || *result.Answers["duplicate"].Noul != 0.8 {
		t.Fatalf("answers = %#v", result.Answers)
	}
	if result.Usage == nil || result.Usage.InputTokens == nil || *result.Usage.InputTokens != 120 || result.Usage.OutputTokens == nil || *result.Usage.OutputTokens != 0 {
		t.Fatalf("usage = %#v", result.Usage)
	}
	if result.Header.Get("X-Request-Id") != "req-123" || result.Header.Get("X-System-One-Credits") != "3" || !strings.Contains(string(result.Answers["team"].Raw), `"extension":true`) || len(result.Raw) == 0 {
		t.Fatalf("response metadata = %#v", result)
	}
}

func TestEvaluateRawPreservesRequestBytes(t *testing.T) {
	requestBody := json.RawMessage("{\n  \"state\": {\"precise\": 9007199254740993}, \"questions\": {\"ok\": {\"type\": \"noul\"}}, \"extension\": 1e-400\n}")
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received, _ = io.ReadAll(request.Body)
		_, _ = io.WriteString(writer, `{"model":"jev-latest","answers":{"ok":{"type":"noul","noul":1}}}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL + "/v1", APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.EvaluateRaw(t.Context(), requestBody, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if string(received) != string(requestBody) {
		t.Fatalf("raw body changed: %q", received)
	}
}

func TestListModelsUsesNativeCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/models" || request.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("model request = %s %s %#v", request.Method, request.URL.Path, request.Header)
		}
		_, _ = io.WriteString(writer, `{"models":[{"name":"jev-latest","description":"Jev","release_date":"2026-09-18","extension":true}]}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL + "/v1", APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.ListModels(t.Context())
	if err != nil || len(result.Models) != 1 || result.Models[0].Name != "jev-latest" || !strings.Contains(string(result.Raw), `"extension":true`) {
		t.Fatalf("models = %#v, err = %v", result, err)
	}
}

func TestAPIErrorRetainsClassificationWithoutPrintingBody(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.Header().Set("X-System-One-Error-Code", "request_in_progress")
		writer.Header().Set("X-System-One-Error-Source", "platform")
		writer.Header().Set("X-Request-Id", "req-456")
		writer.Header().Set("Retry-After", "2")
		writer.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(writer, `{"error":{"message":"private customer text"}}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL + "/v1", APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.EvaluateRaw(t.Context(), json.RawMessage(`{"state":null,"questions":{"ok":{"type":"noul"}}}`), CallOptions{IdempotencyKey: "ticket-123"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict || apiErr.Code != "request_in_progress" || apiErr.Source != "platform" || apiErr.RequestID != "req-456" || apiErr.RetryAfter != "2" || !strings.Contains(string(apiErr.Body), "private customer text") || strings.Contains(apiErr.Error(), "private customer text") || calls.Load() != 1 {
		t.Fatalf("error = %#v, calls = %d", err, calls.Load())
	}
}

func TestNewValidatesConfigurationAndIdempotencyKey(t *testing.T) {
	t.Setenv("SYSTEM_ONE_API_KEY", "")
	if _, err := New(Config{}); err == nil {
		t.Fatal("missing API key was accepted")
	}
	if _, err := New(Config{BaseURL: "https://user:password@example.com/v1", APIKey: "test-key"}); err == nil {
		t.Fatal("URL credentials were accepted")
	}
	for _, key := range []string{"-invalid", "space key", strings.Repeat("x", 129)} {
		if err := validateIdempotencyKey(key); err == nil {
			t.Fatalf("invalid idempotency key %q was accepted", key)
		}
	}
}

func TestClientDoesNotForwardKeyThroughRedirect(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirected.Store(true)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL + "/v1", APIKey: "test-key", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ListModels(t.Context())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTemporaryRedirect || redirected.Load() {
		t.Fatalf("redirect result = %v, target called = %v", err, redirected.Load())
	}
}
