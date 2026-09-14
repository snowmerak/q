package subagent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
)

type CustomRunner struct {
	Client                               AgentClient
	Tools                                ToolRuntime
	Spec                                 Spec
	Profile                              Profile
	Source                               string
	AgentID                              string
	WorkingDirectory, Environment, RunID string
	Sink                                 RecordSink
	Progress                             ProgressFunc
	Trace                                TraceFunc
	MaxRounds                            int
}

func CustomToolAllowed(name string) bool {
	switch name {
	case "task_start", "task_complete", "submit_plan", "submit_brief", "review_task", "ask_to_user", "delegate", "delegate_list", "delegate_scout":
		return false
	}
	return !strings.HasPrefix(name, "delegate_")
}
func SelectCustomTools(p Profile, runtime ToolRuntime) ([]client.Tool, error) {
	var result []client.Tool
	for _, name := range p.Tools {
		found := false
		if runtime != nil && CustomToolAllowed(name) {
			for _, t := range runtime.Tools() {
				if t.Function.Name == name {
					result = append(result, t)
					found = true
					break
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("subagent %s: unavailable tool %q", p.Name, name)
		}
	}
	return result, nil
}
func (r CustomRunner) Run(ctx context.Context, input string) (output string, runErr error) {
	if ctx == nil || r.Client == nil {
		return "", errors.New("custom subagent requires context and client")
	}
	if err := r.Profile.Validate(); err != nil {
		return "", err
	}
	if r.Profile.EffectiveKind() != AgentKindInner {
		return "", errors.New("custom runner requires an inner subagent profile")
	}
	if strings.TrimSpace(input) == "" {
		return "", errors.New("subagent request is required")
	}
	if r.Spec.Role != r.Profile.Role {
		return "", errors.New("profile role does not match resolved model")
	}
	id := strings.TrimSpace(r.AgentID)
	if id == "" {
		id = CanonicalProfileID("global", r.Profile.Name)
	}
	definition := AgentDefinition{
		Info:         DelegateInfo{Name: id, Description: r.Profile.Description, Source: r.Source, Kind: AgentKindInner, Role: r.Profile.Role, MutatesWorkspace: profileMayMutate(r.Profile.Tools)},
		SystemPrompt: r.Profile.SystemPrompt, Tools: append([]string(nil), r.Profile.Tools...),
		Delegates: append([]string(nil), r.Profile.Delegates...), StrictTools: true,
	}
	result, err := (GeneralRunner{
		Client: r.Client, Tools: r.Tools, Spec: r.Spec, Definition: definition,
		WorkingDirectory: r.WorkingDirectory, Environment: r.Environment, RunID: r.RunID,
		Sink: r.Sink, Progress: r.Progress, Trace: r.Trace, MaxRounds: r.MaxRounds,
	}).Run(ctx, input)
	if err != nil {
		return "", err
	}
	return RenderTaskResult(result), nil
}

func RenderTaskResult(result TaskResult) string {
	var body strings.Builder
	body.WriteString(result.Summary)
	writePlanList(&body, "Findings", result.Findings)
	writePlanList(&body, "Artifacts", result.Artifacts)
	writePlanList(&body, "Verification", result.Verification)
	if result.Blocker != "" {
		body.WriteString("\n\nBlocker: ")
		body.WriteString(result.Blocker)
	}
	return body.String()
}
