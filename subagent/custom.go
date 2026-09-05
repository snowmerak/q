package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/sessionstore"
)

type CustomRunner struct {
	Client                               AgentClient
	Tools                                ToolRuntime
	Spec                                 Spec
	Profile                              Profile
	Source                               string
	WorkingDirectory, Environment, RunID string
	Sink                                 RecordSink
	Progress                             ProgressFunc
	Trace                                TraceFunc
	MaxRounds                            int
}

func CustomToolAllowed(name string) bool {
	switch name {
	case "task_complete", "submit_plan", "submit_brief", "submit_debug_report", "submit_review_report", "review_task", "ask_to_user", "delegate_scout":
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
	if strings.TrimSpace(input) == "" {
		return "", errors.New("subagent request is required")
	}
	if r.Spec.Role != r.Profile.Role {
		return "", errors.New("profile role does not match resolved model")
	}
	available, err := SelectCustomTools(r.Profile, r.Tools)
	if err != nil {
		return "", err
	}
	id, err := sessionstore.NewID()
	if err != nil {
		return "", err
	}
	id = "custom-" + r.Profile.Name + "-" + id
	progress := func(action, detail string) {
		reportProgress(r.Progress, ProgressEvent{Agent: r.Profile.Name, TaskID: id, Action: action, Detail: detail})
	}
	progress(ProgressStarted, input)
	var lifecycle *Lifecycle
	if r.Sink != nil {
		lifecycle, err = NewLifecycle(r.Sink, r.RunID, id, "", &r.Spec)
		if err != nil {
			return "", err
		}
		snapshot, _ := json.Marshal(map[string]any{"profile": r.Profile, "source": r.Source, "request": input, "model": r.Spec.Model, "candidates": r.Spec.Candidates})
		if err = lifecycle.Queued(string(snapshot)); err != nil {
			return "", err
		}
		if err = lifecycle.Started(""); err != nil {
			return "", err
		}
	}
	defer func() {
		if runErr != nil {
			progress(ProgressFailed, runErr.Error())
			if lifecycle != nil {
				if errors.Is(runErr, context.Canceled) {
					runErr = errors.Join(runErr, lifecycle.Cancelled("Canceled"))
				} else {
					runErr = errors.Join(runErr, lifecycle.Failed(runErr))
				}
			}
		} else {
			if lifecycle != nil {
				runErr = lifecycle.Succeeded(output, output, map[string]any{"profile": r.Profile, "output": output})
			}
			if runErr != nil {
				progress(ProgressFailed, runErr.Error())
			} else {
				progress(ProgressCompleted, "Completed")
			}
		}
	}()
	systemPrompt := r.Profile.SystemPrompt + "\n\nRuntime environment: " + r.Environment + "\nWorking directory: " + r.WorkingDirectory
	messages := []client.Message{{Role: client.RoleSystem, Content: systemPrompt}, {Role: client.RoleUser, Content: input}}
	history := NewContextCompactor(r.Spec, messages, available, len(messages))
	rounds := r.MaxRounds
	if rounds <= 0 {
		rounds = 320
	}
	empty := false
	for round := 0; round < rounds; round++ {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		if err = history.CompactIfNeeded(ctx, &r.Spec, r.Client); err != nil {
			return "", err
		}
		progress(ProgressThinking, fmt.Sprintf("model round %d", round+1))
		request := client.ChatRequest{Messages: history.RequestMessages(), WorkingDirectory: r.WorkingDirectory}
		if len(available) > 0 {
			parallel := false
			request.Tools = available
			request.ToolChoice = client.ToolChoiceAuto
			request.ParallelToolCalls = &parallel
		}
		response, err := r.Spec.Chat(ctx, r.Client, request)
		if err != nil {
			return "", err
		}
		assistant, err := scoutAssistantMessage(response)
		if err != nil {
			return "", err
		}
		history.Observe(response.Usage)
		history.Append(assistant)
		traceAssistant(r.Trace, r.Profile.Name, id, "", assistant)
		if lifecycle != nil {
			if err = lifecycle.Message(assistant); err != nil {
				return "", err
			}
		}
		if len(assistant.ToolCalls) == 0 {
			if strings.TrimSpace(assistant.TextContent()) != "" {
				return assistant.TextContent(), nil
			}
			if empty {
				return "", errors.New("custom subagent returned repeated empty responses")
			}
			empty = true
			history.Append(client.Message{Role: client.RoleSystem, Content: "Return a non-empty final response or call an available tool."})
			continue
		}
		for _, call := range assistant.ToolCalls {
			if err = ctx.Err(); err != nil {
				return "", err
			}
			progress(ProgressTool, call.Function.Name)
			var result client.ToolResult
			if !hasTool(available, call.Function.Name) {
				result = scoutToolError(fmt.Errorf("tool %q is not selected", call.Function.Name))
			} else {
				result, err = r.Tools.Call(ctx, call)
				if err != nil {
					if ctx.Err() != nil {
						return "", ctx.Err()
					}
					result = scoutToolError(err)
				}
			}
			traceToolResult(r.Trace, r.Profile.Name, id, "", call, result)
			message := client.Message{Role: client.RoleTool, Name: call.Function.Name, ToolCallID: call.ID, Content: result.Content}
			history.Append(message)
			if lifecycle != nil {
				if err = lifecycle.Message(message); err != nil {
					return "", err
				}
			}
		}
	}
	return "", fmt.Errorf("custom subagent exceeded %d model rounds", rounds)
}
