package app

import (
	"context"
	"errors"
	"sync"

	"github.com/snowmerak/q/diagnostics"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
)

type delegationHostKey struct{}
type delegationStartKey struct{}
type delegationStart struct {
	directory string
	ready     chan DelegationRunSnapshot
}

// DelegationRunSnapshot identifies the current execution of a child session.
// Commands must name its ID so a stale browser cannot control a later execution.
type DelegationRunSnapshot struct {
	RequestID string `json:"request_id,omitempty"`
	ID        string `json:"id"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

type delegationRunControl struct {
	mu       sync.Mutex
	control  *subagent.RunControl
	cancel   context.CancelCauseFunc
	snapshot DelegationRunSnapshot
	finished bool
}

func (run *delegationRunControl) status() DelegationRunSnapshot {
	run.mu.Lock()
	defer run.mu.Unlock()
	result := run.snapshot
	if !run.finished {
		result.Status = run.control.Status()
	}
	return result
}

func attachDelegationControl(ctx context.Context, store workspace.Store) (context.Context, *subagent.RunControl, func(error), error) {
	host, ok := ctx.Value(delegationHostKey{}).(*SessionHost)
	if !ok {
		return ctx, nil, func(error) {}, nil
	}
	id, err := workspace.NewSessionID()
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	control := subagent.NewRunControl(cancel)
	run := &delegationRunControl{control: control, cancel: cancel, snapshot: DelegationRunSnapshot{ID: id, Status: "running", RequestID: diagnostics.RequestID(ctx)}}
	host.delegationMu.Lock()
	if host.delegationRuns == nil {
		host.delegationRuns = make(map[string]*delegationRunControl)
	}
	if previous := host.delegationRuns[store.SessionDir()]; previous != nil {
		status := previous.status().Status
		if status == "running" || status == "paused" || status == "cancelling" {
			host.delegationMu.Unlock()
			cancel(nil)
			return nil, nil, nil, errors.New("subagent already has an active execution")
		}
	}
	host.delegationRuns[store.SessionDir()] = run
	host.delegationMu.Unlock()
	if start, ok := ctx.Value(delegationStartKey{}).(delegationStart); ok && start.directory == store.SessionDir() {
		start.ready <- run.status()
	}
	finish := func(err error) {
		run.mu.Lock()
		defer run.mu.Unlock()
		control.Finish()
		run.finished = true
		run.snapshot.Status = control.Status()
		if err != nil && run.snapshot.Status != "cancelled" {
			run.snapshot.Status, run.snapshot.Error = "failed", err.Error()
		}
		cancel(nil)
	}
	return ctx, control, finish, nil
}

func (host *SessionHost) DelegationRun(store workspace.Store) (DelegationRunSnapshot, bool) {
	host.delegationMu.Lock()
	run := host.delegationRuns[store.SessionDir()]
	host.delegationMu.Unlock()
	if run == nil {
		return DelegationRunSnapshot{}, false
	}
	return run.status(), true
}

func (host *SessionHost) ControlDelegation(ctx context.Context, store workspace.Store, runID, action, content string) error {
	state, err := store.LoadDelegationState()
	if err != nil {
		return err
	}
	if DelegationKind(state) != subagent.AgentKindInner {
		return errors.New("external ACP sessions do not support interactive control")
	}
	host.delegationMu.Lock()
	run := host.delegationRuns[store.SessionDir()]
	host.delegationMu.Unlock()
	if run == nil || runID == "" || run.status().ID != runID {
		return errors.New("the selected subagent execution is no longer active")
	}
	switch action {
	case "guidance":
		return run.control.Guide(ctx, content)
	case "pause":
		return run.control.Pause()
	case "resume":
		return run.control.Resume()
	case "cancel":
		return run.control.Cancel()
	default:
		return errors.New("action must be guidance, pause, resume, or cancel")
	}
}

// DelegationKind preserves the kind recorded at execution time and recognizes
// checkpoints written before that field existed. Unknown profiles stay read only.
func DelegationKind(state workspace.DelegationState) string {
	if state.Kind != "" {
		return state.Kind
	}
	if state.Model != "" {
		return subagent.AgentKindInner
	}
	for _, definition := range subagent.PublicAgentDefinitions() {
		if definition.Info.Name == state.Agent {
			return definition.Info.Kind
		}
	}
	return subagent.AgentKindExternal
}

// StartDelegation rejects legacy direct execution while retaining the host API
// used by existing clients. Historical child sessions remain readable.
func (host *SessionHost) StartDelegation(ctx context.Context, root workspace.Store, path, prompt string) (DelegationRunSnapshot, error) {
	return DelegationRunSnapshot{}, errors.New(directDelegationRetired)
}
