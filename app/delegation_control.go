package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/snowmerak/q/client"
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
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
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
	run := &delegationRunControl{control: control, cancel: cancel, snapshot: DelegationRunSnapshot{ID: id, Status: "running"}}
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

// StartDelegation continues an existing inner conversation under the root
// session lock. The already delivered parent tool result remains unchanged.
func (host *SessionHost) StartDelegation(ctx context.Context, root workspace.Store, path, prompt string) (DelegationRunSnapshot, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" || len(prompt) > subagent.MaximumDelegatePromptBytes {
		return DelegationRunSnapshot{}, errors.New("content must be nonempty and within the prompt size limit")
	}
	host.delegationMu.Lock()
	if host.delegationClosing {
		host.delegationMu.Unlock()
		return DelegationRunSnapshot{}, errors.New("session host is shutting down")
	}
	if host.delegationCtx == nil {
		host.delegationCtx, host.delegationCancel = context.WithCancel(host.ctx)
	}
	runContext := context.WithValue(host.delegationCtx, delegationHostKey{}, host)
	host.delegationWG.Add(1)
	host.delegationMu.Unlock()

	prepared, err := host.prepareSession(runContext, root, root.SessionID)
	if err != nil {
		host.delegationWG.Done()
		if errors.Is(err, workspace.ErrLocked) {
			return DelegationRunSnapshot{}, fmt.Errorf("wait for the root session's current run to finish before starting a followup: %w", err)
		}
		return DelegationRunSnapshot{}, err
	}
	started := make(chan DelegationRunSnapshot, 1)
	done := make(chan error, 1)
	go func() {
		defer host.delegationWG.Done()
		err := host.continueDelegation(runContext, prepared, path, prompt, started)
		done <- errors.Join(err, prepared.Close())
	}()
	select {
	case run := <-started:
		return run, nil
	case err := <-done:
		if err == nil {
			select {
			case run := <-started:
				return run, nil
			default:
				err = errors.New("subagent did not start")
			}
		}
		return DelegationRunSnapshot{}, err
	case <-ctx.Done():
		return DelegationRunSnapshot{}, ctx.Err()
	}
}

func (host *SessionHost) continueDelegation(ctx context.Context, prepared *preparedSession, path, prompt string, ready chan DelegationRunSnapshot) error {
	parts := strings.Split(path, "/")
	if path == "" || len(parts) > maximumDelegationDepth {
		return errors.New("invalid delegation path")
	}
	parent := prepared.store
	var child workspace.Store
	var bookmark workspace.DelegationBookmark
	var stack []string
	checkout := prepared.store.Root
	for index, id := range parts {
		bookmarks, err := parent.LoadDelegations()
		if err != nil {
			return err
		}
		found := false
		for _, candidate := range bookmarks {
			if candidate.InvocationID == id {
				bookmark, found = candidate, true
				break
			}
		}
		if !found {
			return workspace.ErrDelegationNotFound
		}
		child, err = parent.ChildStore(id)
		if err != nil {
			return err
		}
		if bookmark.WorkingDirectory != "" {
			checkout = bookmark.WorkingDirectory
		}
		if index < len(parts)-1 {
			state, err := child.LoadDelegationState()
			if err != nil {
				return err
			}
			if state.ChangeRequest != nil && state.ChangeRequest.WorktreePath != "" {
				checkout = state.ChangeRequest.WorktreePath
			}
			stack = append(stack, bookmark.Agent)
			parent = child
		}
	}
	state, err := child.LoadDelegationState()
	if err != nil {
		return err
	}
	if DelegationKind(state) != subagent.AgentKindInner {
		return errors.New("external ACP sessions do not support interactive control")
	}
	if state.Agent != bookmark.Agent || state.Prompt != bookmark.Prompt || state.RunID != bookmark.RunID || state.WorkingDirectory != bookmark.WorkingDirectory {
		return errors.New("delegation state does not match bookmark")
	}
	parentSession, err := parent.Load()
	if err != nil {
		return err
	}
	resultIndex := bookmark.CallIndex + 1 + bookmark.ToolIndex
	if resultIndex >= len(parentSession.Transcript) || parentSession.Transcript[resultIndex].Role != client.RoleTool || parentSession.Transcript[resultIndex].ToolCallID != bookmark.CallID {
		return errors.New("resume the parent session before continuing this unfinished delegation")
	}
	base, err := prepared.state.configuredDelegationRuntime(prepared.state.toolRuntime, prepared.store.Root)
	if err != nil {
		return err
	}
	runtime, ok := base.(*delegationRuntime)
	if !ok {
		return ErrSessionRuntimeUnavailable
	}
	dispatcher := runtime.dispatcher
	if checkout != prepared.store.Root {
		different, closer, err := dispatcher.forWorkingDirectory(ctx, checkout)
		if err != nil {
			return err
		}
		defer closer.Close()
		dispatcher = different
	}
	definition, found := dispatcher.registry.Get(bookmark.Agent)
	if !found || definition.Info.Kind != subagent.AgentKindInner {
		return fmt.Errorf("inner subagent %q is no longer available", bookmark.Agent)
	}
	caller := ""
	if len(stack) > 0 {
		caller = stack[len(stack)-1]
	}
	if !dispatcher.registry.CanDelegate(caller, bookmark.Agent) || !dispatcher.available(bookmark.Agent) {
		return fmt.Errorf("inner subagent %q is no longer allowed or available", bookmark.Agent)
	}
	input := delegateInput{SubagentName: bookmark.Agent, Prompt: bookmark.Prompt, WorkingDirectory: bookmark.WorkingDirectory}
	arguments, err := json.Marshal(input)
	if err != nil {
		return err
	}
	call := client.ToolCall{ID: bookmark.CallID, Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: subagent.DelegateToolName, Arguments: string(arguments)}}
	ctx = context.WithValue(ctx, delegationStartKey{}, delegationStart{directory: child.SessionDir(), ready: ready})
	result, err := dispatcher.runStoredInner(ctx, definition, stack, bookmark, child, state, nil, call, state.ParentID, prompt)
	if err == nil && result.IsError {
		return errors.New(result.Content)
	}
	return err
}
