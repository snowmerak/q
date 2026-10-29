// Package coordinator connects Library-owned workspaces to Q's existing
// session execution and persistence. It does not implement another agent loop.
package coordinator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/workspace"
)

const (
	RoleCoordinator = "coordinator"
	RoleIntegrator  = "integrator"
)

// SessionRole identifies the two reserved conversations. ManagerSessionID is
// retained as the storage/API compatibility name for the Integrator identity.
func SessionRole(project workitems.Workspace, sessionID string) string {
	if sessionID != "" && sessionID == project.ManagerSessionID {
		return RoleIntegrator
	}
	if sessionID != "" && sessionID == project.CoordinatorSessionID {
		return RoleCoordinator
	}
	return ""
}

// RoleInstructions applies to both native and ACP conversations. Execution
// remains on the existing loops and System One still chooses ticket workers.
func RoleInstructions(role string) string {
	switch role {
	case RoleCoordinator:
		return "You are this workspace's Coordinator. Receive user requests, investigate requirements and project context, and record decisions and plans in issues. Break requested work into actionable tickets with linked issues, clear acceptance criteria and an explicit execution_mode. Issued tickets are automatically assigned by System One to suitable joined subagents. Delegate implementation through tickets rather than implementing it yourself. Planning work may use report tickets; create dependent implementation tickets only after the required plan is available, since issued tickets can run concurrently. Track progress through issues and tickets. The separate Integrator reviews results, requests changes and merges code. Do not perform ticket reviews or merges, and do not describe queued work as completed."
	case RoleIntegrator:
		return "You are this workspace's Integrator. Review worker results and pinned changes against ticket acceptance criteria using ticket_attempt_get, ticket_worker_read and ticket_change_read. Use ticket_review to accept, request_changes with actionable feedback, or reject. Accepting a Git result queues its pinned merge; the ticket is complete only when that merge succeeds. Record review evidence and decisions in the issue or ticket. Leave request intake, planning and ticket decomposition to the separate Coordinator. Do not bypass worker review by implementing the ticket yourself."
	default:
		return ""
	}
}

// ManagerStore returns the stable state address independently of project roots.
func ManagerStore(configDirectory string, project workitems.Workspace) (workspace.Store, error) {
	if uuid.Validate(project.ID) != nil || uuid.Validate(project.ManagerSessionID) != nil {
		return workspace.Store{}, errors.New("coordinator: invalid workspace or manager identity")
	}
	root, err := filepath.Abs(filepath.Join(configDirectory, "workspaces", project.ID))
	if err != nil {
		return workspace.Store{}, err
	}
	return workspace.Store{Root: root, SessionID: project.ManagerSessionID}, nil
}

// EnsureManager creates or repairs the reserved manager without replacing history.
func EnsureManager(ctx context.Context, configDirectory string, project workitems.Workspace) (workspace.Store, error) {
	store, err := ManagerStore(configDirectory, project)
	if err != nil {
		return workspace.Store{}, err
	}
	return workspace.EnsureManagerSession(ctx, store.Root, project.ID, project.ManagerSessionID, "Integrator")
}

// CoordinatorStore uses separate storage to preserve the original Integrator's
// protection marker, transcript, model selection and ACP checkpoint in place.
func CoordinatorStore(configDirectory string, project workitems.Workspace) (workspace.Store, error) {
	store, err := ManagerStore(configDirectory, project)
	if err != nil {
		return workspace.Store{}, err
	}
	if uuid.Validate(project.CoordinatorSessionID) != nil || project.CoordinatorSessionID == project.ManagerSessionID {
		return workspace.Store{}, errors.New("coordinator: invalid coordinator identity")
	}
	store.Root = filepath.Join(store.Root, "coordinator")
	store.SessionID = project.CoordinatorSessionID
	return store, nil
}

func EnsureCoordinator(ctx context.Context, configDirectory string, project workitems.Workspace) (workspace.Store, error) {
	store, err := CoordinatorStore(configDirectory, project)
	if err != nil {
		return workspace.Store{}, err
	}
	return workspace.EnsureManagerSession(ctx, store.Root, project.ID, store.SessionID, "Coordinator")
}

// ManagerWorkspace resolves current roots on every turn. The local marker
// grants no authority: the root and reserved identity must match Library too.
func ManagerWorkspace(ctx context.Context, configDirectory string, store workspace.Store) (_ workitems.Workspace, found bool, returnErr error) {
	binding, err := store.ManagerBinding()
	if err != nil || binding == nil {
		return workitems.Workspace{}, false, err
	}
	runtime, err := library.Ensure(ctx, configDirectory)
	if err != nil {
		return workitems.Workspace{}, false, err
	}
	defer func() { returnErr = errors.Join(returnErr, runtime.Close()) }()
	projects, err := runtime.Client().ListWorkspaces(ctx, false)
	if err != nil {
		return workitems.Workspace{}, false, err
	}
	for _, project := range projects.Workspaces {
		if project.ID != binding.WorkspaceID || SessionRole(project, store.SessionID) == "" {
			continue
		}
		expected, err := ManagerStore(configDirectory, project)
		if SessionRole(project, store.SessionID) == RoleCoordinator {
			expected, err = CoordinatorStore(configDirectory, project)
		}
		if err != nil {
			return workitems.Workspace{}, false, err
		}
		left, leftErr := os.Stat(expected.Root)
		right, rightErr := os.Stat(store.Root)
		if leftErr != nil || rightErr != nil || !os.SameFile(left, right) {
			return workitems.Workspace{}, false, errors.New("coordinator: manager storage root does not match Library")
		}
		return project, true, nil
	}
	return workitems.Workspace{}, false, fmt.Errorf("coordinator: manager workspace %s is archived or unavailable", binding.WorkspaceID)
}
