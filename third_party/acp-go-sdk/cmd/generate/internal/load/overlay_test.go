package load

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyForkOverlay(t *testing.T) {
	schemaDir := filepath.Join("..", "..", "..", "..", "schema")
	schema, found, err := ReadSchemaUnstable(schemaDir)
	if err != nil || !found {
		t.Fatalf("load schema: found=%v err=%v", found, err)
	}
	for i := 0; i < 2; i++ {
		if err := ApplyForkOverlay(schemaDir, schema); err != nil {
			t.Fatal(err)
		}
	}
	request := schema.Defs["CreateElicitationRequest"]
	for _, field := range []string{"sessionId", "requestId", "toolCallId"} {
		if request.Properties[field] == nil {
			t.Errorf("missing %s", field)
		}
		for _, required := range request.Required {
			if required == field {
				t.Errorf("%s must be optional for the alternate scopes", field)
			}
		}
	}
}

func TestApplyForkOverlay_RejectsDrift(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target *Definition
	}{
		{"missing", nil},
		{"null", &Definition{Properties: map[string]*Definition{"sessionId": nil}}},
		{"changed type", &Definition{Properties: map[string]*Definition{"sessionId": {Type: "integer"}}}},
		{"newly required", &Definition{Properties: map[string]*Definition{"sessionId": {Ref: "#/$defs/SessionId"}}, Required: []string{"sessionId"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := &Schema{Defs: map[string]*Definition{"CreateElicitationRequest": tc.target}}
			if err := ApplyForkOverlay(filepath.Join("..", "..", "..", "..", "schema"), schema); err == nil {
				t.Fatal("expected upstream drift error")
			}
		})
	}
}

func TestApplyForkOverlay_RejectsInvalidOverlay(t *testing.T) {
	for _, data := range []string{
		`{`, `{}`, `{"$defs":{"CreateElicitationRequest":null}}`,
		`{"$defs":{"CreateElicitationRequest":{"required":["sessionId"]}}}`,
		`{"$defs":{"CreateElicitationRequest":{"properties":{"sessionId":null}}}}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "q-overrides.json"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		schema := &Schema{Defs: map[string]*Definition{"CreateElicitationRequest": {}}}
		if err := ApplyForkOverlay(dir, schema); err == nil {
			t.Fatalf("accepted invalid overlay: %s", data)
		}
	}
	if err := ApplyForkOverlay(t.TempDir(), &Schema{}); err == nil {
		t.Fatal("missing overlay must not silently drop fork behavior")
	}
}
