package main

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestRunSprintCommandRequiresANonEmptyRequirement(t *testing.T) {
	runner := func(context.Context, string, io.Writer) error {
		t.Fatal("runner called for invalid arguments")
		return nil
	}
	for _, args := range [][]string{nil, {}, {""}, {" ", "  "}} {
		err := runSprintCommandWith(t.Context(), args, io.Discard, runner)
		if err == nil || !strings.Contains(err.Error(), "usage: q sprint") {
			t.Fatalf("args=%q error=%v", args, err)
		}
	}
}

func TestRunSprintCommandPassesTrimmedRequirementAndOutput(t *testing.T) {
	var gotObjective string
	var gotOutput io.Writer
	wantOutput := &strings.Builder{}
	err := runSprintCommandWith(t.Context(), []string{"  implement", "the", "feature  "}, wantOutput,
		func(_ context.Context, objective string, output io.Writer) error {
			gotObjective, gotOutput = objective, output
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if gotObjective != "implement the feature" || gotOutput != wantOutput {
		t.Fatalf("objective=%q output=%T", gotObjective, gotOutput)
	}
}
