// Package task implements Q v2's 7 built-in tool categories, 3-mode
// ApprovalGate, Loom large-output artifacts, MCP bridges, and browser control.
package task

import (
	"context"

	"github.com/snowmerak/q/v2/pkg/domain"
)

type RiskClass string

const (
	RiskRead     RiskClass = "read"
	RiskWrite    RiskClass = "write"
	RiskExec     RiskClass = "exec"
	RiskExternal RiskClass = "external"
)

type ToolOutput struct {
	Content      string
	IsError      bool
	ArtifactID   string
	PauseKind    string // "" | "awaiting_user"
	UserQuestion *domain.UserQuestion
}

type CallContext struct {
	RoomID             string
	TurnID             string
	ActorID            string
	ActorName          string
	UserRequest        string
	ApprovalMode       domain.ApprovalMode
	AllowedRoots       []string
	DataDir            string
	DelegationDepth    int
	MaxDelegationDepth int
	ProgressFn         func(message string)
	SubagentFn         func(ctx context.Context, prompt, actorName string, depth int) (string, error)
}

type ToolSpec struct {
	Name             string
	Description      string
	Source           domain.ToolSource
	Risk             RiskClass
	Parameters       map[string]any
	SkipApprovalGate bool
	Handler          func(ctx context.Context, call CallContext, argsJSON string) (ToolOutput, error)
}
