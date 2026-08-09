package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

type targetToolRuntime struct {
	values map[string][]string
	calls  []client.ToolCall
}

func (r *targetToolRuntime) Tools() []client.Tool { return nil }

func (r *targetToolRuntime) Call(_ context.Context, call client.ToolCall) (client.ToolResult, error) {
	r.calls = append(r.calls, call)
	if call.Function.Name != "loom_eval" {
		return client.ToolResult{}, fmt.Errorf("unexpected tool %q", call.Function.Name)
	}
	var arguments struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
		return client.ToolResult{}, err
	}
	value, ok := r.values[arguments.Code]
	if !ok {
		return client.ToolResult{}, fmt.Errorf("unknown Loom selector %q", arguments.Code)
	}
	body, err := json.Marshal(map[string]any{
		"artifact": map[string]any{"ref": "loom://0123456789abcdef0123456789abcdef"},
		"value":    value,
	})
	return client.ToolResult{Content: string(body)}, err
}

func TestTargetConditionValidatesUnionOfIntersections(t *testing.T) {
	target := TargetCondition{Any: []TargetProduct{
		{All: []TargetSelector{
			{Kind: TargetSelectorPaths, Paths: []string{"app", "app"}},
			{Kind: TargetSelectorLoom, Code: `return ["app/model.go"];`, Inputs: map[string]string{
				"tree": "loom://0123456789abcdef0123456789abcdef",
			}},
		}},
		{All: []TargetSelector{{Kind: TargetSelectorPaths, Paths: []string{"README.md"}}}},
	}}
	if err := validateTargetCondition(&target); err != nil {
		t.Fatal(err)
	}
	if len(target.Any[0].All[0].Paths) != 1 {
		t.Fatalf("target paths were not normalized: %#v", target)
	}
	if rendered := renderTargetCondition(target); !strings.Contains(rendered, "AND") || !strings.Contains(rendered, "OR") {
		t.Fatalf("target rendering = %q", rendered)
	}

	invalid := TargetCondition{Any: []TargetProduct{{All: []TargetSelector{{
		Kind: TargetSelectorLoom, Code: `return [];`, Inputs: map[string]string{"tree": "not-a-ref"},
	}}}}}
	if err := validateTargetCondition(&invalid); err == nil {
		t.Fatal("invalid Loom input was accepted")
	}
	outside := TargetCondition{Any: []TargetProduct{{All: []TargetSelector{{
		Kind: TargetSelectorPaths, Paths: []string{"../outside.go"},
	}}}}}
	if err := validateTargetCondition(&outside); err == nil {
		t.Fatal("workspace-escaping path was accepted")
	}
}

func TestTargetResolverEvaluatesUnionOfIntersections(t *testing.T) {
	tools := &targetToolRuntime{values: map[string][]string{
		"select-go": {"app\\model.go", "app/other.go"},
	}}
	target := TargetCondition{Any: []TargetProduct{
		{All: []TargetSelector{
			{Kind: TargetSelectorPaths, Paths: []string{"app/model.go", "docs/readme.md"}},
			{Kind: TargetSelectorLoom, Code: "select-go", Inputs: map[string]string{
				"tree": "loom://0123456789abcdef0123456789abcdef",
			}},
		}},
		{All: []TargetSelector{{Kind: TargetSelectorPaths, Paths: []string{"README.md"}}}},
	}}
	resolved, err := (TargetResolver{Tools: tools}).Resolve(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"README.md", "app/model.go"}; !reflect.DeepEqual(resolved, want) {
		t.Fatalf("resolved targets = %#v, want %#v", resolved, want)
	}
	if len(tools.calls) != 1 || tools.calls[0].Function.Name != "loom_eval" {
		t.Fatalf("tool calls = %#v", tools.calls)
	}
}

func TestExecutionLoopRetriesSameTaskAndAdvancesOnNext(t *testing.T) {
	plan := executableTestPlan()
	plan.Steps = append(plan.Steps, PlanStep{
		Title: "Verify model", Description: "Verify the approved change",
		Target: TargetCondition{Any: []TargetProduct{{All: []TargetSelector{{
			Kind: TargetSelectorPaths, Paths: []string{"app/model_test.go"},
		}}}}},
	})
	var attempts []CoderAttempt
	loop := ExecutionLoop{
		Resolver: TargetResolver{},
		Coder: func(_ context.Context, attempt CoderAttempt) (CoderResult, error) {
			attempts = append(attempts, attempt)
			return CoderResult{Outcome: "succeeded", Summary: "attempt complete"}, nil
		},
		Review: func(_ context.Context, request TaskReviewRequest) (TaskReview, error) {
			switch {
			case request.TaskIndex == 0 && request.Attempt == 1:
				return TaskReview{Decision: "retry", Feedback: "", Facts: []string{"first fact"}}, nil
			case request.TaskIndex == 0 && request.Attempt == 2:
				return TaskReview{Decision: "retry", Feedback: "preserve generated code", Facts: []string{"second fact"}}, nil
			default:
				return TaskReview{Decision: "next", Feedback: ""}, nil
			}
		},
	}
	result, err := loop.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.CompletedTasks != 2 || result.Attempts != 4 {
		t.Fatalf("execution result = %#v", result)
	}
	if got := []int{attempts[0].TaskIndex, attempts[1].TaskIndex, attempts[2].TaskIndex, attempts[3].TaskIndex}; !reflect.DeepEqual(got, []int{0, 0, 0, 1}) {
		t.Fatalf("task sequence = %#v", got)
	}
	if attempts[1].Feedback != "" || attempts[2].Feedback != "preserve generated code" {
		t.Fatalf("retry feedback = %q, %q", attempts[1].Feedback, attempts[2].Feedback)
	}
	if !reflect.DeepEqual(attempts[2].Plan.Facts, []string{"first fact", "second fact"}) ||
		!reflect.DeepEqual(attempts[3].Plan.Facts, []string{"first fact", "second fact"}) {
		t.Fatalf("facts were not carried into later attempts: %#v", attempts)
	}
}

func TestExecutionLoopStopsAfterRetryLimit(t *testing.T) {
	loop := ExecutionLoop{
		Resolver: TargetResolver{}, MaxAttempts: 2,
		Coder: func(context.Context, CoderAttempt) (CoderResult, error) {
			return CoderResult{Outcome: "succeeded", Summary: "not accepted"}, nil
		},
		Review: func(context.Context, TaskReviewRequest) (TaskReview, error) {
			return TaskReview{Decision: "retry", Feedback: ""}, nil
		},
	}
	result, err := loop.Run(context.Background(), executableTestPlan())
	if err == nil || !strings.Contains(err.Error(), "exhausted 2 attempts") {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if result.Attempts != 2 || result.CompletedTasks != 0 {
		t.Fatalf("execution result = %#v", result)
	}
}

func TestPlannerTargetFeedsExecutionLoop(t *testing.T) {
	plannerClient := &fakeScoutClient{responses: []client.Message{{
		Role: client.RoleAssistant,
		ToolCalls: []client.ToolCall{scoutCall(SubmitPlanToolName, `{
			"outcome":"succeeded",
			"summary":"Update and verify the selected Go files",
			"conditions":["Only files selected by the task target may be changed"],
			"steps":[
				{"title":"Update selected files","description":"Apply the change","target":{"any":[
					{"all":[
						{"kind":"paths","paths":["app/model.go","app/view.go"]},
						{"kind":"loom","code":"changed-go","inputs":{"diff":"loom://0123456789abcdef0123456789abcdef"}}
					]},
					{"all":[{"kind":"paths","paths":["README.md"]}]}
				]}},
				{"title":"Verify tests","description":"Run focused verification","target":{"any":[
					{"all":[{"kind":"paths","paths":["app/model_test.go"]}]}
				]}}
			],
			"verification":["go test ./..."]
		}`)},
	}}}
	planner := PlannerRunner{
		Client: plannerClient,
		Spec:   Spec{Role: config.AgentRolePlanner, Model: "planner-model"},
	}
	plan, err := planner.Run(context.Background(), GrillBrief{
		Objective:          "Update selected Go files",
		Conditions:         []string{"Only relevant files are changed"},
		AcceptanceCriteria: []string{"Tests pass"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := &targetToolRuntime{values: map[string][]string{
		"changed-go": {"app/view.go", "app/unrelated.go"},
	}}
	var sequence []string
	loop := ExecutionLoop{
		Resolver: TargetResolver{Tools: tools},
		Coder: func(_ context.Context, attempt CoderAttempt) (CoderResult, error) {
			sequence = append(sequence, fmt.Sprintf("coder:%d:%d:%s", attempt.TaskIndex+1, attempt.Attempt, strings.Join(attempt.Targets, ",")))
			return CoderResult{Outcome: "succeeded", Summary: "done"}, nil
		},
		Review: func(_ context.Context, request TaskReviewRequest) (TaskReview, error) {
			sequence = append(sequence, fmt.Sprintf("review:%d:%d", request.TaskIndex+1, request.Attempt))
			if request.TaskIndex == 0 && request.Attempt == 1 {
				return TaskReview{Decision: "retry", Feedback: ""}, nil
			}
			return TaskReview{Decision: "next", Feedback: ""}, nil
		},
	}
	result, err := loop.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"coder:1:1:README.md,app/view.go", "review:1:1",
		"coder:1:2:README.md,app/view.go", "review:1:2",
		"coder:2:1:app/model_test.go", "review:2:1",
	}
	if !reflect.DeepEqual(sequence, want) {
		t.Fatalf("execution sequence = %#v, want %#v", sequence, want)
	}
	if result.CompletedTasks != 2 || result.Attempts != 3 {
		t.Fatalf("execution result = %#v", result)
	}
}

func TestTaskReviewRequiresFeedbackButAllowsEmptyRetry(t *testing.T) {
	review, err := parseTaskReview(`{"decision":"retry","feedback":"","facts":["The package uses generated files","The package uses generated files"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if review.Decision != "retry" || review.Feedback != "" || len(review.Facts) != 1 {
		t.Fatalf("review = %#v", review)
	}
	if _, err := parseTaskReview(`{"decision":"retry"}`); err == nil || !strings.Contains(err.Error(), "feedback is required") {
		t.Fatalf("missing feedback error = %v", err)
	}
	if _, err := parseTaskReview(`{"decision":"retry_with_feedback","feedback":"fix it"}`); err == nil {
		t.Fatal("a third review transition was accepted")
	}
}

func TestPlannerReviewUpdatesPlanFactsAndCoderPromptCarriesCurrentPlan(t *testing.T) {
	plan := executableTestPlan()
	fake := &fakeScoutClient{responses: []client.Message{{
		Role: client.RoleAssistant,
		ToolCalls: []client.ToolCall{scoutCall(ReviewTaskToolName, `{
			"decision":"retry",
			"feedback":"Preserve the generated section",
			"facts":["app/model.go contains a generated section"]
		}`)},
	}}}
	runner := PlannerReviewRunner{
		Client: fake, Spec: Spec{Role: config.AgentRolePlanner, Model: "planner-model"},
	}
	review, err := runner.Run(context.Background(), TaskReviewRequest{
		Plan: plan, TaskIndex: 0, Attempt: 1,
		Result: CoderResult{Outcome: "succeeded", Summary: "Updated the target"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if review.Decision != "retry" || !strings.Contains(review.Feedback, "generated") {
		t.Fatalf("review = %#v", review)
	}
	if len(fake.requests) != 1 || fake.requests[0].ToolChoice == nil ||
		!hasScoutTool(fake.requests[0].Tools, ReviewTaskToolName) {
		t.Fatalf("review request = %#v", fake.requests)
	}
	if err := ApplyTaskReview(&plan, review); err != nil {
		t.Fatal(err)
	}
	prompt, err := CoderSystemPrompt(plan, 0, 2, []string{"app/model.go"}, review.Feedback)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`"plan"`, `"facts"`, "app/model.go contains a generated section",
		`"task_index": 0`, `"attempt": 2`, "Preserve the generated section",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("Coder system prompt does not contain %q:\n%s", expected, prompt)
		}
	}
}

func executableTestPlan() PlanProposal {
	return PlanProposal{
		Outcome: "succeeded", Summary: "Update the application", Conditions: []string{"Preserve behavior"},
		Steps: []PlanStep{{
			Title: "Update model", Description: "Apply the approved change",
			Target: TargetCondition{Any: []TargetProduct{{All: []TargetSelector{{
				Kind: TargetSelectorPaths, Paths: []string{"app/model.go"},
			}}}}},
		}},
		Verification: []string{"go test ./..."},
	}
}
