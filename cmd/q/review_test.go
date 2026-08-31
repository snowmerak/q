package main

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestRunReviewCommandAllowsDefaultRequest(t *testing.T) {
	var gotRequest string
	var gotOutput io.Writer
	wantOutput := &strings.Builder{}
	err := runReviewCommandWith(t.Context(), nil, wantOutput,
		func(_ context.Context, request string, output io.Writer) error {
			gotRequest, gotOutput = request, output
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if gotRequest != "" || gotOutput != wantOutput {
		t.Fatalf("request=%q output=%T", gotRequest, gotOutput)
	}
}

func TestRunReviewCommandPassesTrimmedRequest(t *testing.T) {
	var got string
	err := runReviewCommandWith(t.Context(), []string{"  focus", "on", "concurrency  "}, io.Discard,
		func(_ context.Context, request string, _ io.Writer) error {
			got = request
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if got != "focus on concurrency" {
		t.Fatalf("request = %q", got)
	}
}
