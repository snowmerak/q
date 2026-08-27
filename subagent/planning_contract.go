package subagent

import "github.com/snowmerak/q/client"

// These examples are part of the model's instructions and are checked against
// both the advertised schema and the runtime validator in tests.
const plannerSucceededExample = `{
  "outcome": "succeeded",
  "summary": "Add a persistent visitor counter",
  "conditions": ["Keep the count across restarts"],
  "steps": [{
    "title": "Implement and test the counter",
    "description": "Add durable counter storage, expose the agreed API, and test persistence across restarts.",
    "target": {"any": [{"all": [{"kind": "paths", "paths": ["src/counter.py", "tests/test_counter.py"]}]}]}
  }],
  "verification": ["Run the counter tests, including the restart persistence case"],
  "blocker": ""
}`

const plannerBlockedExample = `{
  "outcome": "blocked",
  "summary": "The increment trigger is not established",
  "conditions": [],
  "steps": [],
  "verification": [],
  "blocker": "Confirm whether every page request or each unique visitor increments the counter."
}`

func submitPlanTool() client.Tool {
	strict := true
	codeSchema := planTextSchema("JavaScript function body returning an array of workspace-relative file paths; at most 262144 UTF-8 bytes.")
	codeSchema["maxLength"] = maximumTargetCodeBytes
	refSchema := planTextSchema("A valid loom:// artifact reference supplied in the brief.")
	refSchema["pattern"] = `^loom://[0-9a-fA-F]{32}$`
	selectorSchema := map[string]any{
		"description": "Choose one selector kind. Prefer paths; use loom only with actual artifact references supplied in the brief.",
		"anyOf": []map[string]any{
			{
				"type": "object", "properties": map[string]any{
					"kind":  map[string]any{"type": "string", "enum": []string{TargetSelectorPaths}},
					"paths": planStringListSchema("Explicit workspace-relative file paths. New files are allowed. No absolute paths, parent traversal outside the workspace, or glob expansion.", 1),
				}, "required": []string{"kind", "paths"}, "additionalProperties": false,
			},
			{
				"type": "object", "properties": map[string]any{
					"kind": map[string]any{"type": "string", "enum": []string{TargetSelectorLoom}},
					"code": codeSchema,
					"inputs": map[string]any{
						"type": "object", "minProperties": 1,
						"description":          "At least one non-blank input name mapped to an existing loom:// artifact reference from the brief. Do not invent references.",
						"propertyNames":        planTextSchema("Non-blank input name."),
						"additionalProperties": refSchema,
					},
				}, "required": []string{"kind", "code", "inputs"}, "additionalProperties": false,
			},
		},
	}
	targetSchema := map[string]any{
		"type":        "object",
		"description": "File selection only, not execution order. Usually one any group containing one all selector of kind paths is sufficient.",
		"properties": map[string]any{
			"any": map[string]any{
				"type": "array", "minItems": 1, "maxItems": maximumTargetGroups,
				"description": "Union (OR) of file sets selected by the groups.",
				"items": map[string]any{
					"type": "object", "properties": map[string]any{
						"all": map[string]any{
							"type": "array", "minItems": 1, "maxItems": maximumTargetSelectors,
							"description": "Intersection (AND) of independently selected file sets.", "items": selectorSchema,
						},
					}, "required": []string{"all"}, "additionalProperties": false,
				},
			},
		}, "required": []string{"any"}, "additionalProperties": false,
	}
	// Keep a single root object shape for both outcomes. State conditional
	// non-empty requirements in descriptions and enforce them in the parser;
	// blocked submissions use empty arrays instead of fabricated plan steps.
	// The parser still accepts legacy proposals that omitted unused fields.
	return client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
		Name:        SubmitPlanToolName,
		Description: "Submit one complete proposal. For succeeded, conditions, steps, and overall verification must be non-empty. For blocked, use empty arrays and a precise blocker. Call this tool alone; on validation failure correct every reported field and resubmit the full object.",
		Strict:      &strict,
		Parameters: map[string]any{
			"type": "object", "properties": map[string]any{
				"outcome":     map[string]any{"type": "string", "enum": []string{"succeeded", "blocked"}},
				"summary":     planTextSchema("Concise user-visible summary of the plan or the planning blocker."),
				"conditions":  planStringListSchema("Confirmed requirements from the brief. At least one item for succeeded; empty for blocked.", 0),
				"facts":       planStringListSchema("Confirmed repository facts every Coder should know; omit when none are established.", 0),
				"assumptions": planStringListSchema("Explicit assumptions, not confirmed facts.", 0),
				"non_goals":   planStringListSchema("Work explicitly outside this plan's scope.", 0),
				"steps": map[string]any{
					"type": "array", "description": "Ordered executable tasks. At least one task for succeeded; empty for blocked.",
					"items": map[string]any{
						"type": "object", "properties": map[string]any{
							"title":        planTextSchema("Short task title."),
							"description":  planTextSchema("Concrete work and expected result for this task."),
							"target":       targetSchema,
							"verification": planStringListSchema("Optional task-specific checks. These do not replace top-level verification.", 0),
						}, "required": []string{"title", "description", "target"}, "additionalProperties": false,
					},
				},
				"verification": planStringListSchema("Overall acceptance checks. At least one item for succeeded even when steps have their own checks; empty for blocked.", 0),
				"risks":        planStringListSchema("Material risks and relevant mitigations, when known.", 0),
				"blocker":      map[string]any{"type": "string", "description": "Non-blank explanation of what prevents responsible planning for blocked; empty string for succeeded."},
			}, "required": []string{"outcome", "summary", "conditions", "steps", "verification", "blocker"}, "additionalProperties": false,
		},
	}}
}

func planTextSchema(description string) map[string]any {
	return map[string]any{"type": "string", "minLength": 1, "pattern": `\S`, "description": description}
}

func planStringListSchema(description string, minimum int) map[string]any {
	return map[string]any{"type": "array", "minItems": minimum, "items": planTextSchema("Non-blank text."), "description": description}
}
