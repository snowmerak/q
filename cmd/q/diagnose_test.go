package main

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestRunDiagnoseCommandRequiresANonEmptyIssue(t *testing.T) {
	runner := func(context.Context, string, io.Writer) error {
		t.Fatal("runner called for invalid arguments")
		return nil
	}
	for _, args := range [][]string{nil, {}, {""}, {" ", "  "}} {
		err := runDiagnoseCommandWith(t.Context(), args, io.Discard, runner)
		if err == nil || !strings.Contains(err.Error(), "usage: q diagnose") {
			t.Fatalf("args=%q error=%v", args, err)
		}
	}
}

func TestRunDiagnoseCommandPassesTrimmedIssueAndOutput(t *testing.T) {
	var gotIssue string
	var gotOutput io.Writer
	wantOutput := &strings.Builder{}
	err := runDiagnoseCommandWith(t.Context(), []string{"  investigate", "the", "failure  "}, wantOutput,
		func(_ context.Context, issue string, output io.Writer) error {
			gotIssue, gotOutput = issue, output
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if gotIssue != "investigate the failure" || gotOutput != wantOutput {
		t.Fatalf("issue=%q output=%T", gotIssue, gotOutput)
	}
}
