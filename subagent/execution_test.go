package subagent

import (
	"context"
	"encoding/json"
	"errors"
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
				return TaskReview{Decision: "retry", Feedback: "", FactChanges: []FactChange{{
					Op: FactChangeAdd, Value: "first fact", Reason: "first attempt established it",
				}}}, nil
			case request.TaskIndex == 0 && request.Attempt == 2:
				return TaskReview{Decision: "retry", Feedback: "preserve generated code", FactChanges: []FactChange{{
					Op: FactChangeAdd, Value: "second fact", Reason: "second attempt established it",
				}}}, nil
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

func TestExecutionLoopResumesInterruptedCoderAsRecoveryAttempt(t *testing.T) {
	var persisted []ExecutionCheckpoint
	first := ExecutionLoop{
		Resolver: TargetResolver{},
		Coder: func(_ context.Context, attempt CoderAttempt) (CoderResult, error) {
			if attempt.Attempt != 1 {
				t.Fatalf("first attempt = %#v", attempt)
			}
			return CoderResult{}, errors.New("simulated process interruption")
		},
		Review: func(context.Context, TaskReviewRequest) (TaskReview, error) {
			return TaskReview{}, errors.New("review should not run")
		},
		Checkpoint: func(_ context.Context, checkpoint ExecutionCheckpoint) error {
			persisted = append(persisted, checkpoint)
			return nil
		},
	}
	_, err := first.Run(context.Background(), executableTestPlan())
	if err == nil || len(persisted) == 0 {
		t.Fatalf("first run error = %v, checkpoints = %#v", err, persisted)
	}
	interrupted := persisted[len(persisted)-1]
	if interrupted.Phase != ExecutionPhaseCoderRunning || interrupted.Attempt != 1 {
		t.Fatalf("interrupted checkpoint = %#v", interrupted)
	}
	resumed, err := PrepareExecutionResume(interrupted)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Phase != ExecutionPhaseCoderPending || resumed.Attempt != 2 || resumed.ResumeCount != 1 ||
		!strings.Contains(resumed.Feedback, "partially changed") {
		t.Fatalf("prepared checkpoint = %#v", resumed)
	}

	var recovery CoderAttempt
	second := ExecutionLoop{
		Resolver: TargetResolver{},
		Coder: func(_ context.Context, attempt CoderAttempt) (CoderResult, error) {
			recovery = attempt
			return CoderResult{Outcome: "succeeded", Summary: "recovered safely"}, nil
		},
		Review: func(context.Context, TaskReviewRequest) (TaskReview, error) {
			return TaskReview{Decision: "next", Feedback: ""}, nil
		},
		Checkpoint: func(_ context.Context, checkpoint ExecutionCheckpoint) error {
			persisted = append(persisted, checkpoint)
			return nil
		},
	}
	result, err := second.RunFrom(context.Background(), resumed)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Attempt != 2 || !strings.Contains(recovery.Feedback, "Inspect the current workspace") {
		t.Fatalf("recovery attempt = %#v", recovery)
	}
	if result.CompletedTasks != 1 || result.Attempts != 1 {
		t.Fatalf("resumed result = %#v", result)
	}
	if final := persisted[len(persisted)-1]; final.Phase != ExecutionPhaseCompleted || final.ResumeCount != 1 {
		t.Fatalf("final checkpoint = %#v", final)
	}
}

func TestExecutionLoopResumesReviewWithoutRepeatingCoder(t *testing.T) {
	var persisted ExecutionCheckpoint
	first := ExecutionLoop{
		Resolver: TargetResolver{},
		Coder: func(context.Context, CoderAttempt) (CoderResult, error) {
			return CoderResult{Outcome: "succeeded", Summary: "workspace already changed"}, nil
		},
		Review: func(context.Context, TaskReviewRequest) (TaskReview, error) {
			return TaskReview{}, errors.New("review service stopped")
		},
		Checkpoint: func(_ context.Context, checkpoint ExecutionCheckpoint) error {
			persisted = checkpoint
			return nil
		},
	}
	if _, err := first.Run(context.Background(), executableTestPlan()); err == nil {
		t.Fatal("first run succeeded")
	}
	if persisted.Phase != ExecutionPhaseReviewPending || persisted.PendingResult == nil {
		t.Fatalf("review checkpoint = %#v", persisted)
	}
	prepared, err := PrepareExecutionResume(persisted)
	if err != nil {
		t.Fatal(err)
	}
	coderCalls := 0
	second := ExecutionLoop{
		Resolver: TargetResolver{},
		Coder: func(context.Context, CoderAttempt) (CoderResult, error) {
			coderCalls++
			return CoderResult{}, errors.New("Coder must not repeat")
		},
		Review: func(_ context.Context, request TaskReviewRequest) (TaskReview, error) {
			if request.Result.Summary != "workspace already changed" {
				t.Fatalf("review request = %#v", request)
			}
			return TaskReview{Decision: "next", Feedback: ""}, nil
		},
	}
	result, err := second.RunFrom(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if coderCalls != 0 || result.CompletedTasks != 1 {
		t.Fatalf("coder calls = %d, result = %#v", coderCalls, result)
	}
}

func TestExecutionLoopDoesNotStartCoderBeforeRunningCheckpointIsDurable(t *testing.T) {
	coderCalls := 0
	loop := ExecutionLoop{
		Resolver: TargetResolver{},
		Coder: func(context.Context, CoderAttempt) (CoderResult, error) {
			coderCalls++
			return CoderResult{Outcome: "succeeded", Summary: "should not run"}, nil
		},
		Review: func(context.Context, TaskReviewRequest) (TaskReview, error) {
			return TaskReview{Decision: "next", Feedback: ""}, nil
		},
		Checkpoint: func(_ context.Context, checkpoint ExecutionCheckpoint) error {
			if checkpoint.Phase == ExecutionPhaseCoderRunning {
				return errors.New("disk unavailable")
			}
			return nil
		},
	}
	_, err := loop.Run(context.Background(), executableTestPlan())
	if err == nil || !strings.Contains(err.Error(), "disk unavailable") {
		t.Fatalf("checkpoint error = %v", err)
	}
	if coderCalls != 0 {
		t.Fatalf("Coder ran %d time(s) before its checkpoint was durable", coderCalls)
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
	review, err := parseTaskReview(`{
		"decision":"retry",
		"feedback":"",
		"fact_changes":[{
			"op":"add",
			"value":"The package uses generated files",
			"reason":"The generated marker was inspected"
		}]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if review.Decision != "retry" || review.Feedback != "" || len(review.FactChanges) != 1 ||
		review.FactChanges[0].Op != FactChangeAdd || review.FactChanges[0].Value != "The package uses generated files" {
		t.Fatalf("review = %#v", review)
	}
	if _, err := parseTaskReview(`{"decision":"retry"}`); err == nil || !strings.Contains(err.Error(), "feedback is required") {
		t.Fatalf("missing feedback error = %v", err)
	}
	if _, err := parseTaskReview(`{"decision":"retry_with_feedback","feedback":"fix it"}`); err == nil {
		t.Fatal("a third review transition was accepted")
	}
	if _, err := parseTaskReview(`{"decision":"retry","feedback":"","facts":["legacy fact"]}`); err == nil {
		t.Fatal("legacy facts field was accepted")
	}
}

func TestReviewTaskToolUsesFactChangesContract(t *testing.T) {
	tool := reviewTaskTool()
	properties, ok := tool.Function.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("review_task properties = %#v", tool.Function.Parameters["properties"])
	}
	if _, exists := properties["facts"]; exists {
		t.Fatalf("review_task still exposes legacy facts: %#v", properties)
	}
	factChanges, ok := properties["fact_changes"].(map[string]any)
	if !ok || factChanges["type"] != "array" {
		t.Fatalf("review_task fact_changes = %#v", properties["fact_changes"])
	}
	items, ok := factChanges["items"].(map[string]any)
	if !ok {
		t.Fatalf("review_task fact_changes items = %#v", factChanges["items"])
	}
	variants, ok := items["anyOf"].([]map[string]any)
	if !ok || len(variants) != 3 {
		t.Fatalf("review_task fact change variants = %#v", items["anyOf"])
	}
}

func TestApplyTaskReviewAppliesFactChangesAtomically(t *testing.T) {
	plan := executableTestPlan()
	plan.Facts = []string{"keep this fact", "replace this fact", "remove this fact"}
	review := TaskReview{Decision: "retry", Feedback: "", FactChanges: []FactChange{
		{Op: FactChangeReplace, Target: "replace this fact", Value: "replacement fact", Reason: "new evidence corrected it"},
		{Op: FactChangeRemove, Target: "remove this fact", Reason: "verification disproved it"},
		{Op: FactChangeAdd, Value: "new fact", Reason: "verification established it"},
		{Op: FactChangeAdd, Value: "keep this fact", Reason: "the same fact was independently confirmed"},
	}}
	if err := ApplyTaskReview(&plan, review); err != nil {
		t.Fatal(err)
	}
	want := []string{"keep this fact", "replacement fact", "new fact"}
	if !reflect.DeepEqual(plan.Facts, want) {
		t.Fatalf("facts = %#v, want %#v", plan.Facts, want)
	}
}

func TestApplyTaskReviewRejectsInvalidFactChangesWithoutMutation(t *testing.T) {
	tests := []struct {
		name    string
		changes []FactChange
		wantErr string
	}{
		{
			name: "missing target",
			changes: []FactChange{{
				Op: FactChangeRemove, Target: "unknown fact", Reason: "it was disproved",
			}},
			wantErr: "does not match an existing fact",
		},
		{
			name: "same target twice",
			changes: []FactChange{
				{Op: FactChangeReplace, Target: "existing fact", Value: "corrected fact", Reason: "it was corrected"},
				{Op: FactChangeRemove, Target: "existing fact", Reason: "it was disproved"},
			},
			wantErr: "targets a fact more than once",
		},
		{
			name: "missing reason",
			changes: []FactChange{{
				Op: FactChangeAdd, Value: "new fact",
			}},
			wantErr: "reason is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := executableTestPlan()
			plan.Facts = []string{"existing fact"}
			err := ApplyTaskReview(&plan, TaskReview{Decision: "retry", Feedback: "", FactChanges: tt.changes})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
			if !reflect.DeepEqual(plan.Facts, []string{"existing fact"}) {
				t.Fatalf("facts mutated after failed change: %#v", plan.Facts)
			}
		})
	}
}

func TestPlannerReviewRetriesFactChangeWithUnknownTarget(t *testing.T) {
	clientFake := &fakeScoutClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(ReviewTaskToolName, `{
			"decision":"retry",
			"feedback":"",
			"fact_changes":[{
				"op":"remove",
				"target":"unknown fact",
				"reason":"the attempt disproved it"
			}]
		}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(ReviewTaskToolName, `{
			"decision":"next",
			"feedback":""
		}`)}},
	}}
	plan := executableTestPlan()
	plan.Facts = []string{"known fact"}
	review, err := (PlannerReviewRunner{
		Client: clientFake, Spec: Spec{Role: config.AgentRolePlanner, Model: "planner-model"},
	}).Run(t.Context(), TaskReviewRequest{
		Plan: plan, TaskIndex: 0, Attempt: 1,
		Result: CoderResult{Outcome: "succeeded", Summary: "Updated the target"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if review.Decision != "next" || len(clientFake.requests) != 2 {
		t.Fatalf("review = %#v, requests = %#v", review, clientFake.requests)
	}
	messages := clientFake.requests[1].Messages
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1].Content, "does not match an existing fact") {
		t.Fatalf("Planner did not receive fact target error: %#v", messages)
	}
}

func TestPlannerReviewCanInspectBoundedCoderEvidence(t *testing.T) {
	loomRef := "loom://0123456789abcdef0123456789abcdef"
	clientFake := &fakeScoutClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall("loom_inspect", `{"ref":"`+loomRef+`"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(ReviewTaskToolName, `{
			"decision":"next",
			"feedback":"",
			"fact_changes":[{
				"op":"add",
				"value":"The edited file was inspected",
				"reason":"bounded evidence identified the edited file"
			}]
		}`)}},
	}}
	toolResult := client.ToolResult{Content: `{"ref":"` + loomRef + `","bytes":128}`}
	tools := &fakeScoutTools{available: []client.Tool{
		scoutFunctionTool("read_file"), scoutFunctionTool("edit_file"),
		scoutFunctionTool("loom_inspect"), scoutFunctionTool("loom_read"), scoutFunctionTool("loom_eval"),
		scoutFunctionTool("run_command"), scoutFunctionTool("cmd_status"), scoutFunctionTool("wait"),
	}, result: &toolResult}
	runner := PlannerReviewRunner{
		Client: clientFake, Tools: tools, Spec: Spec{Role: config.AgentRolePlanner, Model: "planner-model"},
		WorkingDirectory: `C:\workspace`,
	}
	review, err := runner.Run(context.Background(), TaskReviewRequest{
		Plan: executableTestPlan(), TaskIndex: 0, Attempt: 1, Targets: []string{"app/model.go"},
		Result: CoderResult{
			Outcome: "succeeded", Summary: "Updated the model",
			Evidence: []CoderEvidence{{Tool: "edit_file", LoomRef: loomRef, Paths: []string{"app/model.go"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if review.Decision != "next" || len(tools.calls) != 1 || tools.calls[0].Function.Name != "loom_inspect" {
		t.Fatalf("review = %#v, tool calls = %#v", review, tools.calls)
	}
	if len(clientFake.requests) != 2 || !strings.Contains(clientFake.requests[0].Messages[1].Content, loomRef) ||
		!strings.Contains(clientFake.requests[0].Messages[1].Content, `"resolved_targets"`) {
		t.Fatalf("review requests = %#v", clientFake.requests)
	}
	for _, name := range []string{"read_file", "loom_inspect", "loom_read", "loom_eval", "run_command", "wait", ReviewTaskToolName} {
		if !hasScoutTool(clientFake.requests[0].Tools, name) {
			t.Fatalf("Planner review tool %q missing: %#v", name, clientFake.requests[0].Tools)
		}
	}
	for _, name := range []string{"edit_file", "cmd_status", ExternalSearchToolName} {
		if hasScoutTool(clientFake.requests[0].Tools, name) {
			t.Fatalf("Planner review received disallowed tool %q: %#v", name, clientFake.requests[0].Tools)
		}
	}
}

func TestPlannerReviewCallsExternalSearchAndReceivesCapturedResult(t *testing.T) {
	model := &fakeScoutClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(ExternalSearchToolName,
			`{"query":"current protocol compatibility","completion_criteria":["cite the specification"]}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall(ReviewTaskToolName,
			`{"decision":"next","feedback":"","fact_changes":[{"op":"add","value":"The implementation matches the current protocol","reason":"the current specification confirms compatibility"}]}`)}},
	}}
	var received ExternalSearchInput
	tools := testExternalSearchRuntime(t, &received, ExternalSearchResult{
		Agent: "search-agent", Summary: "The current protocol supports this implementation", Sources: []string{"https://example.test/spec"},
	})
	review, err := (PlannerReviewRunner{
		Client: model, Tools: tools, Spec: Spec{Role: config.AgentRolePlanner, Model: "planner-model"},
	}).Run(t.Context(), TaskReviewRequest{
		Plan: executableTestPlan(), TaskIndex: 0, Attempt: 1, Targets: []string{"app/model.go"},
		Result: CoderResult{Outcome: "succeeded", Summary: "Implemented protocol support"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if received.Query != "current protocol compatibility" || review.Decision != "next" || len(model.requests) != 2 {
		t.Fatalf("input=%+v review=%+v requests=%d", received, review, len(model.requests))
	}
	if !hasTool(model.requests[0].Tools, ExternalSearchToolName) {
		t.Fatal("external_search was not exposed to Planner review")
	}
	messages := model.requests[1].Messages
	result := messages[len(messages)-1]
	if result.Role != client.RoleTool || result.Name != ExternalSearchToolName ||
		!strings.Contains(result.Content, "current protocol supports") || !strings.Contains(result.Content, "loom_ref") {
		t.Fatalf("review did not receive captured search evidence: %+v", result)
	}
}

func TestPlannerReviewUpdatesPlanFactsAndCoderPromptCarriesCurrentPlan(t *testing.T) {
	plan := executableTestPlan()
	fake := &fakeScoutClient{responses: []client.Message{{
		Role: client.RoleAssistant,
		ToolCalls: []client.ToolCall{scoutCall(ReviewTaskToolName, `{
			"decision":"retry",
			"feedback":"Preserve the generated section",
			"fact_changes":[{
				"op":"add",
				"value":"app/model.go contains a generated section",
				"reason":"the generated marker was inspected"
			}]
		}`)},
	}}}
	sink := &scoutRecordSink{}
	runner := PlannerReviewRunner{
		Client: fake, Spec: Spec{Role: config.AgentRolePlanner, Model: "planner-model"},
		Sink: sink, RunID: "run-review-1", ExecutionID: "execution-review-1",
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
	if len(sink.records) < 4 || sink.records[len(sink.records)-1].Status != "succeeded" ||
		sink.records[len(sink.records)-1].Role != config.AgentRolePlanner {
		t.Fatalf("Planner review lifecycle = %#v", sink.records)
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
