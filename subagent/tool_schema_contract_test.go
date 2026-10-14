package subagent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
)

func subagentSchemaCatalog() map[string][]client.Tool {
	return map[string][]client.Tool{
		"external_search": {ExternalSearchTool()}, "external_web_tester": {ExternalWebTesterTool()},
		"lifecycle": TaskLifecycleTools(), "delegation": DelegateTools(), "change_requests": ChangeRequestTools(),
		"grill":   {askUserTool(), delegateScoutTool(), submitBriefTool()},
		"planner": {submitPlanTool(PlanExecutorCoder, PlanExecutorExternalWebTester), reviewTaskTool()},
		"scout":   {scoutCompletionTool()}, "coder": {coderCompletionTool()},
	}
}

// OpenAI's strict subset requires closed objects with every property required,
// including objects nested in arrays, unions, and referenced definitions.
func strictObjectContract(value any, path string) error {
	schema, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if schema["type"] == "object" || schema["properties"] != nil {
		if schema["additionalProperties"] != false {
			return fmt.Errorf("%s: additionalProperties must be false", path)
		}
		required := map[string]bool{}
		names, _ := schema["required"].([]any)
		for _, name := range names {
			if text, ok := name.(string); ok {
				required[text] = true
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for name, child := range properties {
			if !required[name] {
				return fmt.Errorf("%s: required must include property %q", path, name)
			}
			if err := strictObjectContract(child, path+".properties."+name); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"items", "additionalProperties"} {
		if err := strictObjectContract(schema[key], path+"."+key); err != nil {
			return err
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		values, _ := schema[key].([]any)
		for index, child := range values {
			if err := strictObjectContract(child, fmt.Sprintf("%s.%s[%d]", path, key, index)); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"$defs", "definitions"} {
		values, _ := schema[key].(map[string]any)
		for name, child := range values {
			if err := strictObjectContract(child, path+"."+key+"."+name); err != nil {
				return err
			}
		}
	}
	return nil
}

func TestSubagentToolStrictDeclarationsMatchSchemas(t *testing.T) {
	for group, catalog := range subagentSchemaCatalog() {
		for _, tool := range catalog {
			t.Run(group+"/"+tool.Function.Name, func(t *testing.T) {
				if tool.Function.Strict == nil || !*tool.Function.Strict {
					return
				}
				body, err := json.Marshal(tool.Function.Parameters)
				if err != nil {
					t.Fatal(err)
				}
				var schema map[string]any
				if err := json.Unmarshal(body, &schema); err != nil {
					t.Fatal(err)
				}
				if err := strictObjectContract(schema, "parameters"); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestToolCatalogThroughResponsesGateway(t *testing.T) {
	catalog := subagentSchemaCatalog()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"model"}]}`)
			return
		}
		var body struct {
			Stream bool
			Tools  []struct {
				Name       string
				Strict     *bool
				Parameters map[string]any
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(body.Tools) != 1 || body.Tools[0].Strict == nil {
			t.Errorf("tool strictness must be explicit: %+v", body.Tools)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		tool := body.Tools[0]
		if *tool.Strict {
			if err := strictObjectContract(tool.Parameters, "parameters"); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "Invalid schema for function '" + tool.Name + "': " + err.Error()}})
				return
			}
		}
		// Query-only arguments remain valid; optional criteria are not invented.
		terminal := fmt.Sprintf(`{"id":"resp-tools","model":"model","status":"completed","output":[{"type":"function_call","call_id":"call-1","name":%q,"arguments":"{\"query\":\"docs\"}"}]}`, tool.Name)
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", terminal)
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, terminal)
		}
	}))
	t.Cleanup(upstream.Close)
	configuredGateway, err := gateway.New(gateway.Config{Providers: []gateway.ProviderConfig{{ID: "plan", Type: "openai-compatible", Enabled: true, BaseURL: upstream.URL, Models: []string{"model"}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configuredGateway.Close() })
	server := httptest.NewServer(configuredGateway.Handler())
	t.Cleanup(server.Close)
	configured, err := client.New(client.Config{BaseURL: server.URL + "/v1", DisableAPIKey: true, ModelAPIModes: map[string]string{"plan/model": "responses"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configured.Close() })
	for group, catalog := range catalog {
		for _, tool := range catalog {
			for _, streaming := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%v", group, tool.Function.Name, streaming), func(t *testing.T) {
					request := client.ChatRequest{Model: "plan/model", Messages: []client.Message{{Role: client.RoleUser, Content: "hello"}}, Tools: []client.Tool{tool}}
					if streaming {
						stream, err := configured.ChatStream(t.Context(), request)
						if err != nil {
							t.Fatal(err)
						}
						defer func() { _ = stream.Close() }()
						for {
							if _, err := stream.Recv(); err == io.EOF {
								break
							} else if err != nil {
								t.Fatal(err)
							}
						}
					} else {
						response, err := configured.Chat(t.Context(), request)
						if err != nil {
							t.Fatal(err)
						}
						if tool.Function.Name == ExternalSearchToolName {
							input, err := ParseExternalSearchInput(response.Choices[0].Message.ToolCalls[0].Function.Arguments)
							if err != nil || input.Query != "docs" || len(input.CompletionCriteria) != 0 {
								t.Fatalf("optional search criteria changed: %+v, %v", input, err)
							}
						}
					}
				})
			}
		}
	}
}

func TestExternalSearchStillValidatesOptionalInputs(t *testing.T) {
	for _, arguments := range []string{`{"query":"docs"}`, `{"query":"docs","context":["Go"],"completion_criteria":["cite sources"]}`} {
		if _, err := ParseExternalSearchInput(arguments); err != nil {
			t.Fatal(err)
		}
	}
	for _, arguments := range []string{`{}`, `{"query":" "}`, `{"query":"docs","unknown":true}`, `{"query":"docs","completion_criteria":"wrong type"}`} {
		if _, err := ParseExternalSearchInput(arguments); err == nil {
			t.Fatalf("invalid input accepted: %s", arguments)
		}
	}
	if tool := ExternalSearchTool(); tool.Function.Strict == nil || *tool.Function.Strict {
		t.Fatal("external_search must preserve optional fields with explicit non-strict function calling")
	}
}
