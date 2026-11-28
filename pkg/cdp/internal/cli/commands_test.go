package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	qdriver "github.com/snowmerak/q/v2/pkg/cdp"
)

func TestCommandValidationBeforeDispatch(t *testing.T) {
	for _, args := range [][]string{
		{"page", "click", "main"},
		{"page", "fill", "--css", "#name", "--ref", "@1", "main", "value"},
		{"page", "click", "main", "--css", "#name"},
		{"page", "snapshot", "--scope", "subtree", "main"},
		{"page", "snapshot", "--scope", "unknown", "main"},
		{"page", "snapshot", "--max-nodes", "-1", "main"},
		{"page", "click", "--ref", "@1", "--frame", "child", "main"},
		{"events", "arm", "--kind", "unknown", "main"},
		{"chooser", "interception", "main", "maybe"},
		{"page", "upload", "--css", "#file", "main", "relative.txt"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, err := parseCommand(args, strings.NewReader("")); err == nil {
				t.Fatal("accepted invalid CLI arguments")
			}
		})
	}
	r, err := parseCommand([]string{"page", "fill", "--css", "#name", "main", ""}, strings.NewReader(""))
	if err != nil || r.Value != "" || r.Locator.Kind != "css" {
		t.Fatalf("empty fill lost presence: %+v %v", r, err)
	}
	r, err = parseCommand([]string{"page", "eval", "main", "-"}, strings.NewReader("Promise.resolve(42)"))
	if err != nil || r.Value != "Promise.resolve(42)" {
		t.Fatalf("stdin expression: %+v %v", r, err)
	}
	if _, err := parseCommand([]string{"page", "eval", "main", "-"}, strings.NewReader(strings.Repeat("x", (1<<20)+1))); err == nil {
		t.Fatal("stdin budget was ignored")
	}
}

func TestSDKErrorIdentitySurvivesCLITranslation(t *testing.T) {
	err := &qdriver.OperationError{Operation: "click", PageID: "page", MayHaveEffects: true, Err: errors.Join(context.Canceled, qdriver.ErrDialogOpen)}
	out := failure(err, false)
	if out.Code != "dialog_open" || !out.MayHaveEffects || out.PageID != "page" {
		t.Fatalf("modal interruption lost its contract: %+v", out)
	}
}
