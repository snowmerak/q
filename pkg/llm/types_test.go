package llmprovider

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestChatChunkPreservesChoicePhaseOnWire(t *testing.T) {
	chunk := ChatChunk{Choices: []Choice{{
		Index: 0, Phase: "commentary",
		Delta: &Message{Role: RoleAssistant, Content: "working"},
	}}}
	data, err := json.Marshal(chunk)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ChatChunk
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Choices) != 1 || decoded.Choices[0].Phase != "commentary" ||
		decoded.Choices[0].Delta == nil || decoded.Choices[0].Delta.Content != "working" {
		t.Fatalf("chunk = %#v, wire = %s", decoded, data)
	}
}

func TestMessagePhaseDoesNotChangeChatWireFormat(t *testing.T) {
	message := Message{Role: RoleAssistant, Phase: "final_answer", Content: "answer"}
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if _, found := wire["phase"]; found {
		t.Fatalf("Chat message includes Responses phase: %s", data)
	}
	if wire["role"] != "assistant" || wire["content"] != "answer" {
		t.Fatalf("Chat message = %s", data)
	}
}

func TestModelNormalizesContextLengthAliases(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int64
	}{
		{name: "canonical", body: `{"id":"a","context_length":1000}`, want: 1000},
		{name: "anthropic", body: `{"id":"a","max_input_tokens":1500}`, want: 1500},
		{name: "vllm", body: `{"id":"a","max_model_len":2000}`, want: 2000},
		{name: "context window", body: `{"id":"a","context_window":3000}`, want: 3000},
		{name: "token suffix", body: `{"id":"a","context_window_tokens":4000}`, want: 4000},
		{name: "string", body: `{"id":"a","max_context_length":"5000"}`, want: 5000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var model Model
			if err := json.Unmarshal([]byte(test.body), &model); err != nil {
				t.Fatal(err)
			}
			if model.ContextLength != test.want {
				t.Fatalf("context length = %d, want %d", model.ContextLength, test.want)
			}
			data, err := json.Marshal(model)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(data, &wire); err != nil {
				t.Fatal(err)
			}
			if wire["context_length"] != float64(test.want) {
				t.Fatalf("wire model = %s", data)
			}
		})
	}
}

func TestModelNormalizesMaxOutputTokenAliases(t *testing.T) {
	var model Model
	if err := json.Unmarshal([]byte(`{"id":"a","max_tokens":128000}`), &model); err != nil {
		t.Fatal(err)
	}
	if model.MaxOutputTokens != 128000 {
		t.Fatalf("max output tokens = %d", model.MaxOutputTokens)
	}
	data, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["max_output_tokens"] != float64(128000) {
		t.Fatalf("wire model = %s", data)
	}
}

func TestModelPreservesReasoningEffortMetadata(t *testing.T) {
	var model Model
	if err := json.Unmarshal([]byte(`{"id":"a","capabilities":{"reasoning":{"supported":true,"control":"effort","supported_efforts":["low","medium","high"],"default_effort":"medium"}}}`), &model); err != nil {
		t.Fatal(err)
	}
	reasoning := model.Capabilities.Reasoning
	if reasoning.DefaultEffort != "medium" || len(reasoning.SupportedEfforts) != 3 ||
		reasoning.SupportedEfforts[2] != "high" {
		t.Fatalf("model = %#v", model)
	}
	data, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) || string(data) == "" {
		t.Fatalf("model JSON = %s", data)
	}
}

func TestModelNormalizesFlatCapabilityNames(t *testing.T) {
	var model Model
	if err := json.Unmarshal([]byte(`{
		"id":"Qwen3.8-Flash-Next-MLX-Serve-mixed-4-8bit",
		"context_length":262144,
		"capabilities":["chat","tool_use","streaming","vision","reasoning","json_schema"],
		"meta":{"architecture":"qwen4_exp"}
	}`), &model); err != nil {
		t.Fatal(err)
	}
	if model.ID != "Qwen3.8-Flash-Next-MLX-Serve-mixed-4-8bit" || model.ContextLength != 262144 {
		t.Fatalf("model = %#v", model)
	}
	if model.Capabilities == nil || model.Capabilities.Reasoning == nil ||
		!model.Capabilities.Reasoning.Supported ||
		model.Capabilities.Reasoning.Control != ReasoningControlFixed {
		t.Fatalf("capabilities = %#v", model.Capabilities)
	}
	if model.Capabilities.ToolCalling == nil || !*model.Capabilities.ToolCalling {
		t.Fatalf("tool use capability = %v", model.Capabilities.ToolCalling)
	}
}

func TestModelPreservesToolSupportFromCatalog(t *testing.T) {
	supported, unsupported := true, false
	tests := []struct {
		name, body string
		want       *bool
	}{
		{"supported parameter", `{"id":"a","supported_parameters":["tools","temperature"]}`, &supported},
		{"Kitani tool use label", `{"id":"a","capabilities":["chat","tool use"],"supported_parameters":["stream","temperature"]}`, &supported},
		{"parameter absent from list", `{"id":"a","supported_parameters":["temperature"]}`, &unsupported},
		{"empty parameter list", `{"id":"a","supported_parameters":[]}`, &unsupported},
		{"absent metadata", `{"id":"a","capabilities":["chat"]}`, nil},
		{"null parameter list", `{"id":"a","supported_parameters":null}`, nil},
		{"explicit unsupported", `{"id":"a","capabilities":{"tool_calling":false},"supported_parameters":["tools"]}`, &unsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var model Model
			if err := json.Unmarshal([]byte(test.body), &model); err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(model)
			if err != nil {
				t.Fatal(err)
			}
			var gatewayModel Model
			if err := json.Unmarshal(wire, &gatewayModel); err != nil {
				t.Fatal(err)
			}
			var got *bool
			if gatewayModel.Capabilities != nil {
				got = gatewayModel.Capabilities.ToolCalling
			}
			if (got == nil) != (test.want == nil) || got != nil && *got != *test.want {
				t.Fatalf("tool support did not survive round trip: %s", wire)
			}
		})
	}
}

func TestModelPrefersCatalogReasoningEffortsOverFlatTag(t *testing.T) {
	var model Model
	raw := `{"id":"TheDrummer/Artemis-31B-v1.2","capabilities":["chat","reasoning"],"supported_parameters":["stream","reasoning_effort"],"reasoning":{"supported":true,"control":"binary","efforts":["none","high"]}}`
	if err := json.Unmarshal([]byte(raw), &model); err != nil {
		t.Fatal(err)
	}
	capabilities := model.Capabilities
	if capabilities == nil || capabilities.ToolCalling == nil || *capabilities.ToolCalling ||
		capabilities.Reasoning == nil || capabilities.Reasoning.Control != ReasoningControlEffort ||
		!slices.Equal(capabilities.Reasoning.SupportedEfforts, []string{"none", "high"}) {
		t.Fatalf("capabilities = %#v", capabilities)
	}
}

func TestModelNormalizesOpenRouterReasoningMetadata(t *testing.T) {
	var model Model
	if err := json.Unmarshal([]byte(`{"id":"a","reasoning":{"supported_efforts":["high","medium","low","minimal"],"default_effort":"medium","mandatory":true}}`), &model); err != nil {
		t.Fatal(err)
	}
	reasoning := model.Capabilities.Reasoning
	if reasoning.Control != ReasoningControlEffort || reasoning.DefaultEffort != "medium" ||
		len(reasoning.SupportedEfforts) != 4 || reasoning.SupportedEfforts[3] != "minimal" {
		t.Fatalf("model = %#v", model)
	}
}

func TestModelNormalizesOpenRouterReasoningToggle(t *testing.T) {
	var model Model
	if err := json.Unmarshal([]byte(`{"id":"hermes","reasoning":{"default_enabled":false,"mandatory":false}}`), &model); err != nil {
		t.Fatal(err)
	}
	reasoning := model.Capabilities.Reasoning
	if reasoning.Control != ReasoningControlToggle || reasoning.DefaultEnabled == nil || *reasoning.DefaultEnabled {
		t.Fatalf("model = %#v", model)
	}
}

func TestModelNormalizesOpenRouterReasoningTokenBudget(t *testing.T) {
	var model Model
	if err := json.Unmarshal([]byte(`{"id":"budget","reasoning":{"supports_max_tokens":true,"default_enabled":true}}`), &model); err != nil {
		t.Fatal(err)
	}
	reasoning := model.Capabilities.Reasoning
	if reasoning.Control != ReasoningControlTokenBudget || !reasoning.SupportsMaxTokens ||
		reasoning.DefaultEnabled == nil || !*reasoning.DefaultEnabled {
		t.Fatalf("model = %#v", model)
	}
}

func TestMessageDecodesReasoningWithoutSerializingOutgoing(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "reasoning_content",
			raw:  `{"role":"assistant","content":"hi","reasoning_content":"step by step"}`,
			want: "step by step",
		},
		{
			name: "reasoning",
			raw:  `{"role":"assistant","content":"","reasoning":"hidden trace"}`,
			want: "hidden trace",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var msg Message
			if err := json.Unmarshal([]byte(tc.raw), &msg); err != nil {
				t.Fatal(err)
			}
			if msg.Reasoning != tc.want {
				t.Fatalf("Reasoning = %q, want %q", msg.Reasoning, tc.want)
			}
			data, err := json.Marshal(msg)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(data, &wire); err != nil {
				t.Fatal(err)
			}
			if _, ok := wire["reasoning_content"]; ok {
				t.Fatalf("unexpected reasoning_content in wire output: %s", data)
			}
			if _, ok := wire["reasoning"]; ok {
				t.Fatalf("unexpected reasoning in wire output: %s", data)
			}
		})
	}
}
