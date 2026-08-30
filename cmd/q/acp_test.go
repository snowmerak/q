package main

import (
	"io"
	"strings"
	"testing"
)

func TestACPCommandDoesNotExposeConnectClient(t *testing.T) {
	err := runACPCommand(t.Context(), []string{"connect", "codex"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "usage: q acp") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseACPCommandPlanAutomationOverrides(t *testing.T) {
	tests := []struct {
		name                     string
		args                     []string
		approveSet, approveValue bool
		resolveSet, resolveValue bool
	}{
		{name: "defaults"},
		{name: "auto approve", args: []string{"--auto-approve"}, approveSet: true, approveValue: true},
		{name: "auto resolve", args: []string{"--auto-resolve"}, resolveSet: true, resolveValue: true},
		{name: "autonomous", args: []string{"--autonomous"}, approveSet: true, approveValue: true, resolveSet: true, resolveValue: true},
		{name: "individual override wins", args: []string{"--autonomous", "--auto-approve=false"}, approveSet: true, resolveSet: true, resolveValue: true},
		{name: "disable persistent values", args: []string{"--auto-approve=false", "--auto-resolve=false"}, approveSet: true, resolveSet: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, err := parseACPCommandOptions(test.args, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			approve := options.app.Plan.AutoApprove
			resolve := options.app.Plan.AutoResolve
			if approve.Set != test.approveSet || approve.Value != test.approveValue ||
				resolve.Set != test.resolveSet || resolve.Value != test.resolveValue {
				t.Fatalf("plan overrides = approve %#v, resolve %#v", approve, resolve)
			}
		})
	}
}
