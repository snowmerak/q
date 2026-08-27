package load

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// ApplyForkOverlay adds Q's tracked protocol properties before code generation.
// The upstream snapshots remain untouched. Fail on upstream drift instead of
// silently overwriting a new protocol definition with an obsolete fork patch.
func ApplyForkOverlay(schemaDir string, schema *Schema) error {
	data, err := os.ReadFile(filepath.Join(schemaDir, "q-overrides.json"))
	if err != nil {
		return fmt.Errorf("read fork overlay: %w", err)
	}
	var overlay Schema
	if err := json.Unmarshal(data, &overlay); err != nil {
		return fmt.Errorf("parse fork overlay: %w", err)
	}
	if len(overlay.Defs) == 0 {
		return fmt.Errorf("fork overlay has no definitions")
	}
	for name, patch := range overlay.Defs {
		if patch == nil {
			return fmt.Errorf("fork overlay %s is null", name)
		}
		// Only additive properties are supported; changing unions, required fields,
		// or method names must be an explicit generator/schema migration.
		withoutProperties := *patch
		withoutProperties.Properties = nil
		if !reflect.DeepEqual(withoutProperties, Definition{}) || len(patch.Properties) == 0 {
			return fmt.Errorf("fork overlay %s must contain only additive properties", name)
		}
		if schema == nil || schema.Defs[name] == nil {
			return fmt.Errorf("fork overlay target %s is missing; review the upstream schema", name)
		}
		target := schema.Defs[name]
		if target.Properties == nil {
			target.Properties = make(map[string]*Definition)
		}
		for property, addition := range patch.Properties {
			if addition == nil {
				return fmt.Errorf("fork overlay %s.%s is null", name, property)
			}
			if existing, ok := target.Properties[property]; ok {
				if existing == nil {
					return fmt.Errorf("fork overlay conflicts with upstream %s.%s", name, property)
				}
				left, right := *existing, *addition
				left.Description, right.Description = "", ""
				if !reflect.DeepEqual(left, right) {
					return fmt.Errorf("fork overlay conflicts with upstream %s.%s; review the protocol change", name, property)
				}
				for _, required := range target.Required {
					if required == property {
						return fmt.Errorf("fork overlay %s.%s became required upstream; review the scope contract", name, property)
					}
				}
				continue
			}
			target.Properties[property] = addition
		}
	}
	return nil
}
