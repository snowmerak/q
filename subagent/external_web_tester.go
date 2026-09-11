package subagent

import (
	"errors"
	"strings"

	"github.com/snowmerak/q/client"
)

const ExternalWebTesterToolName = "external_web_tester"

type ExternalWebTesterInput struct {
	Request            string   `json:"request"`
	Context            []string `json:"context,omitempty"`
	CompletionCriteria []string `json:"completion_criteria,omitempty"`
}

type ExternalWebTesterResult struct {
	Agent        string   `json:"agent"`
	Outcome      string   `json:"outcome"`
	Summary      string   `json:"summary"`
	Findings     []string `json:"findings,omitempty"`
	Verification []string `json:"verification,omitempty"`
	Artifacts    []string `json:"artifacts,omitempty"`
	Blocker      string   `json:"blocker,omitempty"`
}

func ExternalWebTesterTool() client.Tool {
	strict := true
	stringsSchema := map[string]any{
		"type": "array", "maxItems": maximumCoderListItems,
		"items": map[string]any{"type": "string", "maxLength": maximumCoderTextBytes},
	}
	return client.Tool{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
		Name:        ExternalWebTesterToolName,
		Description: "Run the configured external web tester against the current workspace and return a structured verification report.",
		Strict:      &strict,
		Parameters: map[string]any{
			"type": "object", "properties": map[string]any{
				"request": map[string]any{"type": "string", "maxLength": maximumCoderTextBytes}, "context": stringsSchema,
				"completion_criteria": stringsSchema,
			},
			"required": []string{"request"}, "additionalProperties": false,
		},
	}}
}

func ParseExternalWebTesterInput(arguments string) (ExternalWebTesterInput, error) {
	var input ExternalWebTesterInput
	if err := decodeStrict(arguments, &input); err != nil {
		return ExternalWebTesterInput{}, err
	}
	input.Request = strings.TrimSpace(input.Request)
	input.Context = cleanStrings(input.Context)
	input.CompletionCriteria = cleanStrings(input.CompletionCriteria)
	if input.Request == "" {
		return ExternalWebTesterInput{}, errors.New("external_web_tester request is required")
	}
	if len(input.Request) > maximumCoderTextBytes || !boundedAgentStrings(input.Context) || !boundedAgentStrings(input.CompletionCriteria) {
		return ExternalWebTesterInput{}, errors.New("external_web_tester text or list exceeds the supported bound")
	}
	return input, nil
}

func ParseExternalWebTesterResult(body string) (ExternalWebTesterResult, error) {
	var result ExternalWebTesterResult
	if err := decodeStrict(body, &result); err != nil {
		return ExternalWebTesterResult{}, err
	}
	result.Agent = strings.TrimSpace(result.Agent)
	result.Outcome = strings.TrimSpace(result.Outcome)
	result.Summary = strings.TrimSpace(result.Summary)
	result.Findings = cleanStrings(result.Findings)
	result.Verification = cleanStrings(result.Verification)
	result.Artifacts = cleanStrings(result.Artifacts)
	result.Blocker = strings.TrimSpace(result.Blocker)
	if result.Outcome != "succeeded" && result.Outcome != "failed" && result.Outcome != "blocked" {
		return ExternalWebTesterResult{}, errors.New("external web tester outcome must be succeeded, failed, or blocked")
	}
	if result.Summary == "" {
		return ExternalWebTesterResult{}, errors.New("external web tester summary is required")
	}
	if len(result.Agent) > maximumCoderTextBytes || len(result.Summary) > maximumCoderTextBytes ||
		len(result.Blocker) > maximumCoderTextBytes || !boundedAgentStrings(result.Findings) ||
		!boundedAgentStrings(result.Verification) || !boundedAgentStrings(result.Artifacts) {
		return ExternalWebTesterResult{}, errors.New("external web tester text or list exceeds the supported bound")
	}
	if result.Outcome == "blocked" && result.Blocker == "" {
		return ExternalWebTesterResult{}, errors.New("external web tester blocker is required for outcome blocked")
	}
	if result.Outcome != "blocked" && result.Blocker != "" {
		return ExternalWebTesterResult{}, errors.New("external web tester blocker is only valid for outcome blocked")
	}
	return result, nil
}

func boundedAgentStrings(values []string) bool {
	if len(values) > maximumCoderListItems {
		return false
	}
	for _, value := range values {
		if len(value) > maximumCoderTextBytes {
			return false
		}
	}
	return true
}
