package app

import (
	"context"
	"errors"
	"sync"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/subagent"
)

type planningClient struct {
	mu               sync.Mutex
	responses        []client.Message
	requests         []client.ChatRequest
	terminalRequests []client.ChatRequest
}

func (p *planningClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if request.ToolChoice == client.ToolChoiceNone && len(request.Messages) > 0 && request.Messages[len(request.Messages)-1].Role == client.RoleTool {
		p.terminalRequests = append(p.terminalRequests, request)
		return terminalAcknowledgment(""), nil
	}
	p.requests = append(p.requests, request)
	if len(p.responses) == 0 {
		return nil, errors.New("no planning response")
	}
	message := p.responses[0]
	p.responses = p.responses[1:]
	return &client.ChatResponse{Choices: []client.Choice{{Message: message}}}, nil
}

func (p *planningClient) ListModels(context.Context) ([]client.Model, error) {
	return []client.Model{{ID: "plan-model"}}, nil
}

func (p *planningClient) Close() error { return nil }

func resumablePlanCheckpoint(phase subagent.ExecutionPhase) subagent.ExecutionCheckpoint {
	return subagent.ExecutionCheckpoint{
		ExecutionID: "plan-execution-resume", RunID: "run-resume", Objective: "Persist approved execution",
		Phase: phase, Attempt: 1,
		Plan: subagent.PlanProposal{
			Outcome: "succeeded", Summary: "Resume an approved plan",
			Conditions: []string{"Do not repeat completed side effects"},
			Steps: []subagent.PlanStep{{
				Title: "Connect persistence", Description: "Resume the saved task",
				Target: subagent.TargetCondition{Any: []subagent.TargetProduct{{All: []subagent.TargetSelector{{
					Kind: subagent.TargetSelectorPaths, Paths: []string{"app/plan.go"},
				}}}}},
			}},
		},
	}
}

func planToolCall(name, arguments string) client.ToolCall {
	return client.ToolCall{
		ID: "call-" + name, Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: name, Arguments: arguments},
	}
}
