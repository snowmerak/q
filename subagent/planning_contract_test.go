package subagent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

func resolvePlanSchema(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	body, err := json.Marshal(submitPlanTool().Function.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(body, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func planExampleObject(t *testing.T, example string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(example), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPlannerContractExamplesAreValidAndIncludedInPrompt(t *testing.T) {
	schema := resolvePlanSchema(t)
	for _, example := range []string{plannerSucceededExample, plannerBlockedExample} {
		value := planExampleObject(t, example)
		if err := schema.Validate(value); err != nil {
			t.Fatalf("example does not match advertised schema: %v", err)
		}
		if _, err := parsePlanProposal(example); err != nil {
			t.Fatalf("example does not pass runtime validation: %v", err)
		}
		if !strings.Contains(plannerInstructions(), example) {
			t.Fatal("model prompt does not include the validated example")
		}
	}
	for _, text := range []string{"new files", "overall verification", "1 to 16", "262144 UTF-8 bytes", "resubmit the complete proposal"} {
		if !strings.Contains(plannerInstructions(), text) {
			t.Fatalf("prompt is missing guidance %q", text)
		}
	}
}

func TestSubmitPlanSchemaRequiresStableOutcomeFields(t *testing.T) {
	schema := resolvePlanSchema(t)
	for _, example := range []string{plannerSucceededExample, plannerBlockedExample} {
		for _, field := range []string{"outcome", "summary", "conditions", "steps", "verification", "blocker"} {
			value := planExampleObject(t, example)
			delete(value, field)
			if err := schema.Validate(value); err == nil {
				t.Fatalf("schema accepted missing %s in %s", field, value["outcome"])
			}
		}
	}
	properties := submitPlanTool().Function.Parameters["properties"].(map[string]any)
	for _, field := range []string{"conditions", "steps", "verification", "blocker"} {
		description := properties[field].(map[string]any)["description"].(string)
		if !strings.Contains(description, "succeeded") || !strings.Contains(description, "blocked") {
			t.Fatalf("%s lacks conditional requirements: %q", field, description)
		}
	}
}

func TestSubmitPlanSchemaAndValidatorRejectMalformedTargets(t *testing.T) {
	schema := resolvePlanSchema(t)
	validRef := "loom://0123456789abcdef0123456789abcdef"
	for _, test := range []struct {
		name     string
		selector map[string]any
		valid    bool
	}{
		{"paths", map[string]any{"kind": "paths", "paths": []string{"new/file.go"}}, true},
		{"paths missing", map[string]any{"kind": "paths"}, false},
		{"paths empty", map[string]any{"kind": "paths", "paths": []string{}}, false},
		{"paths blank", map[string]any{"kind": "paths", "paths": []string{" "}}, false},
		{"paths with code", map[string]any{"kind": "paths", "paths": []string{"a.go"}, "code": "return [];"}, false},
		{"loom", map[string]any{"kind": "loom", "code": "return inputs.tree.files;", "inputs": map[string]string{"tree": validRef}}, true},
		{"loom missing code", map[string]any{"kind": "loom", "inputs": map[string]string{"tree": validRef}}, false},
		{"loom blank code", map[string]any{"kind": "loom", "code": " ", "inputs": map[string]string{"tree": validRef}}, false},
		{"loom missing inputs", map[string]any{"kind": "loom", "code": "return [];"}, false},
		{"loom empty inputs", map[string]any{"kind": "loom", "code": "return [];", "inputs": map[string]string{}}, false},
		{"loom invalid ref", map[string]any{"kind": "loom", "code": "return [];", "inputs": map[string]string{"tree": "not-a-ref"}}, false},
		{"loom blank name", map[string]any{"kind": "loom", "code": "return [];", "inputs": map[string]string{" ": validRef}}, false},
		{"loom with paths", map[string]any{"kind": "loom", "code": "return [];", "inputs": map[string]string{"tree": validRef}, "paths": []string{"a.go"}}, false},
		{"loom oversized code", map[string]any{"kind": "loom", "code": strings.Repeat("x", maximumTargetCodeBytes+1), "inputs": map[string]string{"tree": validRef}}, false},
		{"unknown kind", map[string]any{"kind": "glob", "paths": []string{"*.go"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := planExampleObject(t, plannerSucceededExample)
			step := value["steps"].([]any)[0].(map[string]any)
			step["target"] = map[string]any{"any": []any{map[string]any{"all": []any{test.selector}}}}
			body, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			// Decode to ordinary JSON values for the schema validator.
			value = planExampleObject(t, string(body))
			if err := schema.Validate(value); (err == nil) != test.valid {
				t.Fatalf("schema valid=%v, error=%v", test.valid, err)
			}
			if _, err := parsePlanProposal(string(body)); (err == nil) != test.valid {
				t.Fatalf("runtime valid=%v, error=%v", test.valid, err)
			}
		})
	}
}

func TestSubmitPlanTargetBoundsMatchRuntime(t *testing.T) {
	schema := resolvePlanSchema(t)
	for _, test := range []struct{ groups, selectors int }{
		{0, 1}, {1, 0}, {maximumTargetGroups, maximumTargetSelectors},
		{maximumTargetGroups + 1, 1}, {1, maximumTargetSelectors + 1},
	} {
		value := planExampleObject(t, plannerSucceededExample)
		groups := make([]any, test.groups)
		for index := range groups {
			selectors := make([]any, test.selectors)
			for selector := range selectors {
				selectors[selector] = map[string]any{"kind": "paths", "paths": []string{"a.go"}}
			}
			groups[index] = map[string]any{"all": selectors}
		}
		value["steps"].([]any)[0].(map[string]any)["target"] = map[string]any{"any": groups}
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		valid := test.groups > 0 && test.groups <= maximumTargetGroups && test.selectors > 0 && test.selectors <= maximumTargetSelectors
		if err := schema.Validate(planExampleObject(t, string(body))); (err == nil) != valid {
			t.Fatalf("schema bounds %+v: %v", test, err)
		}
		if _, err := parsePlanProposal(string(body)); (err == nil) != valid {
			t.Fatalf("runtime bounds %+v: %v", test, err)
		}
	}
}

const invalidPlanWithMultipleErrors = `{
  "outcome":"succeeded", "summary":" ", "conditions":[], "verification":[],
  "steps":[
    {"title":"", "description":"", "target":{"any":[{"all":[
      {"kind":"paths","paths":["../outside.go"]},
      {"kind":"loom"}
    ]}]}},
    {"title":"", "description":"Do work", "target":{"any":[]}}
  ]
}`

func assertAllPlanErrors(t *testing.T, message string) {
	t.Helper()
	for _, want := range []string{
		"summary:", "conditions:", "verification:", "steps[0].title:", "steps[0].description:",
		"steps[0].target.any[0].all[0].paths[0]:",
		"steps[0].target.any[0].all[1].code:", "steps[0].target.any[0].all[1].inputs:",
		"steps[1].title:", "steps[1].target.any:", "resubmit the complete proposal",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("missing %q in combined feedback:\n%s", want, message)
		}
	}
}

func TestPlanValidationCollectsIndependentErrors(t *testing.T) {
	_, err := parsePlanProposal(invalidPlanWithMultipleErrors)
	if err == nil {
		t.Fatal("invalid plan accepted")
	}
	assertAllPlanErrors(t, err.Error())
	_, err = parsePlanProposal(`{"outcome":"blocked","summary":"","blocker":""}`)
	if err == nil || !strings.Contains(err.Error(), "summary:") || !strings.Contains(err.Error(), "blocker:") {
		t.Fatalf("blocked errors not collected: %v", err)
	}
	_, err = parsePlanProposal(`{"outcome":"succeeded","summary":"Plan"}`)
	if err == nil || !strings.Contains(err.Error(), "conditions:") || !strings.Contains(err.Error(), "steps:") || !strings.Contains(err.Error(), "verification:") {
		t.Fatalf("missing success fields not collected: %v", err)
	}
}

func TestTargetValidationKeepsOriginalPathIndexesAndInvalidInputs(t *testing.T) {
	target := TargetCondition{Any: []TargetProduct{{All: []TargetSelector{
		{Kind: TargetSelectorPaths, Paths: []string{" ", "ok.go", "ok.go", "../outside.go"}},
		{Kind: TargetSelectorLoom, Code: "return inputs.tree.files;", Inputs: map[string]string{
			" tree ":  "loom://0123456789abcdef0123456789abcdef",
			"tree":    "loom://0123456789abcdef0123456789abcdef",
			"invalid": "not-a-ref",
		}},
	}}}}
	err := validateTargetCondition(&target)
	if err == nil || !strings.Contains(err.Error(), "paths[3]:") ||
		!strings.Contains(err.Error(), `inputs["invalid"]:`) || !strings.Contains(err.Error(), "duplicate input name") {
		t.Fatalf("missing or mislocated target errors: %v", err)
	}
	if len(target.Any[0].All[1].Inputs) != 3 {
		t.Fatal("validation discarded invalid input names")
	}
	if err := validateTargetCondition(&target); err == nil || !strings.Contains(err.Error(), "duplicate input name") {
		t.Fatalf("revalidation lost duplicate-input error: %v", err)
	}
}

func TestPlannerRetryReceivesCompleteValidationFeedback(t *testing.T) {
	invalidCall := scoutCall(SubmitPlanToolName, invalidPlanWithMultipleErrors)
	fake := &fakeScoutClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{invalidCall}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(SubmitPlanToolName, plannerSucceededExample)}},
	}}
	plan, err := (PlannerRunner{
		Client: fake, Spec: Spec{Role: config.AgentRolePlanner, Model: "planner"}, MaxRounds: 2,
	}).Run(t.Context(), GrillBrief{Objective: "Implement a durable counter", Conditions: []string{"Keep the count across restarts"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 2 || plan.Outcome != "succeeded" {
		t.Fatalf("requests=%d outcome=%s", len(fake.requests), plan.Outcome)
	}
	if fake.requests[0].Messages[0].Content != plannerInstructions() ||
		!reflect.DeepEqual(fake.requests[0].Tools, []client.Tool{submitPlanTool()}) {
		t.Fatal("planner did not receive the instructions and tool contract")
	}
	messages := fake.requests[1].Messages
	feedback := messages[len(messages)-1]
	if feedback.Role != client.RoleTool || feedback.ToolCallID != invalidCall.ID {
		t.Fatalf("validation feedback not paired with submit_plan: %#v", feedback)
	}
	var result struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(feedback.Content), &result); err != nil {
		t.Fatal(err)
	}
	assertAllPlanErrors(t, result.Error)
}

func TestPlanProposalPreservesLegacyShapesAndNewFileTargets(t *testing.T) {
	for _, body := range []string{
		`{"outcome":"blocked","summary":"Need clarification","blocker":"Confirm scope"}`,
		plannerSucceededExample,
	} {
		// Stored proposals may omit empty arrays or an unused blocker.
		value := planExampleObject(t, body)
		if value["outcome"] == "succeeded" {
			delete(value, "blocker")
		}
		legacy, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parsePlanProposal(string(legacy)); err != nil {
			t.Fatalf("legacy plan rejected: %v", err)
		}
	}
	plan, err := parsePlanProposal(plannerSucceededExample)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := (TargetResolver{}).Resolve(t.Context(), plan.Steps[0].Target)
	if err != nil || !reflect.DeepEqual(paths, []string{"src/counter.py", "tests/test_counter.py"}) {
		t.Fatalf("new-file targets were not retained: paths=%v err=%v", paths, err)
	}
}
