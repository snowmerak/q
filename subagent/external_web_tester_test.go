package subagent

import (
	"strings"
	"testing"
)

func TestExternalWebTesterToolAndInputAreStrictAndBounded(t *testing.T) {
	tool := ExternalWebTesterTool()
	if tool.Function.Name != ExternalWebTesterToolName || tool.Function.Strict == nil || !*tool.Function.Strict {
		t.Fatalf("tool = %#v", tool)
	}
	input, err := ParseExternalWebTesterInput(`{"request":" verify login ","context":[" local app "]}`)
	if err != nil {
		t.Fatal(err)
	}
	if input.Request != "verify login" || len(input.Context) != 1 || input.Context[0] != "local app" {
		t.Fatalf("input = %#v", input)
	}
	if _, err := ParseExternalWebTesterInput(`{"request":"verify","unknown":true}`); err == nil {
		t.Fatal("unknown input field was accepted")
	}
	tooLong := `{"request":"` + strings.Repeat("x", maximumCoderTextBytes+1) + `"}`
	if _, err := ParseExternalWebTesterInput(tooLong); err == nil {
		t.Fatal("oversized request was accepted")
	}
}

func TestExternalWebTesterResultDistinguishesProductFailureAndContractFailure(t *testing.T) {
	result, err := ParseExternalWebTesterResult(`{"outcome":"failed","summary":"login did not persist","findings":["cookie missing"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "failed" || result.Summary == "" {
		t.Fatalf("result = %#v", result)
	}
	for _, body := range []string{
		`{"outcome":"blocked","summary":"server unavailable"}`,
		`{"outcome":"unknown","summary":"bad enum"}`,
		`{"outcome":"succeeded","summary":"ok","extra":true}`,
		`{"outcome":"succeeded","summary":"ok","blocker":"contradiction"}`,
	} {
		if _, err := ParseExternalWebTesterResult(body); err == nil {
			t.Fatalf("invalid result was accepted: %s", body)
		}
	}
	if err := validateTaskResult(TaskResult{
		Executor: PlanExecutorExternalWebTester, Outcome: "failed", Summary: "broken", Blocker: "not blocked",
	}); err == nil {
		t.Fatal("non-blocked checkpoint result accepted a blocker")
	}
}
