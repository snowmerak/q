package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/subagent"
)

func TestExecutionStoreRoundTripAndClear(t *testing.T) {
	store := Store{Root: t.TempDir()}
	want := testExecutionCheckpoint()
	if err := store.SaveExecution(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadExecution()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded checkpoint = %#v, want %#v", got, want)
	}
	info, err := os.Stat(store.ExecutionPath())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("execution permissions are too broad: %o", info.Mode().Perm())
	}
	if err := store.ClearExecution(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadExecution(); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("LoadExecution after clear error = %v", err)
	}
}

func TestExecutionStoreRejectsInvalidAndUnknownFields(t *testing.T) {
	store := Store{Root: t.TempDir()}
	invalid := testExecutionCheckpoint()
	invalid.ExecutionID = ""
	if err := store.SaveExecution(invalid); err == nil {
		t.Fatal("SaveExecution accepted a missing execution ID")
	}
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ExecutionPath(), []byte(`{"version":1,"updated_at":"2026-08-10T00:00:00Z","checkpoint":{},"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadExecution(); err == nil {
		t.Fatal("LoadExecution accepted an unknown field")
	}
}

func TestExecutionStoreMigratesVersionOneCoderCheckpoint(t *testing.T) {
	store := Store{Root: t.TempDir(), SessionID: "session-1"}
	legacy := testExecutionCheckpoint()
	legacy.Plan.Steps[0].Executor = ""
	legacy.Phase = subagent.ExecutionPhase("coder_running")
	legacy.ActiveExecutor = ""
	legacy.Targets = []string{"app/model.go"}
	body, err := json.Marshal(executionFile{
		Version: 1, SessionID: store.SessionID, UpdatedAt: time.Now().UTC(), Checkpoint: legacy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.SessionDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ExecutionPath(), body, 0o600); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := store.LoadExecution()
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Phase != subagent.ExecutionPhaseExecutorRunning ||
		checkpoint.ActiveExecutor != subagent.PlanExecutorCoder ||
		checkpoint.Plan.Steps[0].Executor != subagent.PlanExecutorCoder {
		t.Fatalf("migrated checkpoint = %#v", checkpoint)
	}
	resumed, err := subagent.PrepareExecutionResume(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Phase != subagent.ExecutionPhaseExecutorPending || resumed.Attempt != 2 {
		t.Fatalf("resumed checkpoint = %#v", resumed)
	}
}

func TestExecutionStoreMigratesVersionOnePendingCoderResult(t *testing.T) {
	store := Store{Root: t.TempDir(), SessionID: "session-1"}
	legacy := testExecutionCheckpoint()
	legacy.Plan.Steps[0].Executor = ""
	legacy.Phase = subagent.ExecutionPhaseReviewPending
	legacy.ActiveExecutor = ""
	legacy.Targets = []string{"app/model.go"}
	legacy.TaskAttempts = 1
	legacy.Attempts = 1
	legacy.PendingResult = &subagent.TaskResult{Outcome: "succeeded", Summary: "legacy coder result"}
	body, err := json.Marshal(executionFile{
		Version: 1, SessionID: store.SessionID, UpdatedAt: time.Now().UTC(), Checkpoint: legacy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.SessionDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ExecutionPath(), body, 0o600); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := store.LoadExecution()
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.ActiveExecutor != subagent.PlanExecutorCoder || checkpoint.PendingResult == nil ||
		checkpoint.PendingResult.Executor != subagent.PlanExecutorCoder || checkpoint.PendingResult.Summary != "legacy coder result" {
		t.Fatalf("migrated review checkpoint = %#v", checkpoint)
	}
}

func TestLoomReferencesAtIncludesActivePlanExecution(t *testing.T) {
	store := Store{Root: t.TempDir()}
	checkpoint := testExecutionCheckpoint()
	if err := store.SaveExecution(checkpoint); err != nil {
		t.Fatal(err)
	}
	refs, err := LoomReferencesAt(context.Background(), store.Root)
	if err != nil {
		t.Fatal(err)
	}
	want := []loom.Ref{"loom://0123456789abcdef0123456789abcdef"}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("Loom refs = %#v, want %#v", refs, want)
	}
}

func testExecutionCheckpoint() subagent.ExecutionCheckpoint {
	started := time.Date(2026, time.August, 30, 1, 2, 3, 0, time.UTC)
	return subagent.ExecutionCheckpoint{
		ExecutionID: "plan-execution-1", RunID: "run-1", Objective: "Resume the plan",
		Planning: &subagent.PlanningLog{
			RunID: "run-1", Objective: "Resume the plan", StartedAt: started,
			CompletedAt: started.Add(time.Second), Outcome: subagent.PlanningOutcomeApproved, Cycles: 1,
			Events: []subagent.PlanningEvent{{
				Sequence: 1, At: started, Type: subagent.PlanningEventAssistant,
				Agent: "planner", Content: "I found the execution boundary.",
			}},
		},
		ExecutionLog: &subagent.ExecutionLog{
			StartedAt: started.Add(2 * time.Second),
			Events: []subagent.PlanningEvent{{
				Sequence: 1, At: started.Add(2 * time.Second), Type: subagent.PlanningEventActivity,
				Agent: "executor", Action: subagent.ProgressStarted, Detail: "executing approved plan",
			}},
		},
		Phase: subagent.ExecutionPhaseTarget, Attempt: 1,
		Plan: subagent.PlanProposal{
			Outcome: "succeeded", Summary: "Persist execution",
			Steps: []subagent.PlanStep{{
				Title: "Update files", Description: "Apply the change", Executor: subagent.PlanExecutorCoder,
				Target: subagent.TargetCondition{Any: []subagent.TargetProduct{{All: []subagent.TargetSelector{{
					Kind: subagent.TargetSelectorLoom, Code: `return ["app/model.go"];`,
					Inputs: map[string]string{"tree": "loom://0123456789abcdef0123456789abcdef"},
				}}}}},
			}},
		},
	}
}
