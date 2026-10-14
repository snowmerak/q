package client

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestResponsesFunctionStrictnessIsExplicitAndPreservesSchema(t *testing.T) {
	yes, no := true, false
	for _, test := range []struct {
		name   string
		strict *bool
		want   bool
	}{{"default", nil, false}, {"disabled", &no, false}, {"enabled", &yes, true}} {
		t.Run(test.name, func(t *testing.T) {
			parameters := map[string]any{
				"type": "object", "properties": map[string]any{
					"query": map[string]any{"type": "string"}, "optional": map[string]any{"type": "string"},
				}, "required": []string{"query"}, "additionalProperties": false,
			}
			if test.want {
				parameters["required"] = []string{"query", "optional"}
			}
			before, err := json.Marshal(parameters)
			if err != nil {
				t.Fatal(err)
			}
			configured := &Client{}
			body, err := configured.responseRequest(ChatRequest{Model: "test", Tools: []Tool{{Type: ToolTypeFunction, Function: FunctionDefinition{Name: "lookup", Strict: test.strict, Parameters: parameters}}}}, "")
			if err != nil {
				t.Fatal(err)
			}
			var sent struct {
				Tools []struct {
					Strict     *bool
					Parameters json.RawMessage
				}
			}
			if err := json.Unmarshal(body, &sent); err != nil {
				t.Fatal(err)
			}
			if len(sent.Tools) != 1 || sent.Tools[0].Strict == nil || *sent.Tools[0].Strict != test.want {
				t.Fatalf("strictness changed: %s", body)
			}
			after, err := json.Marshal(parameters)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || !bytes.Equal(before, sent.Tools[0].Parameters) {
				t.Fatalf("caller schema changed: before=%s, after=%s, sent=%s", before, after, body)
			}
		})
	}
}
