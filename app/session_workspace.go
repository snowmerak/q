package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/snowmerak/q/coordinator"
	"github.com/snowmerak/q/workspace"
)

// resolveSessionExecution keeps persistence rooted at Store.Root while tools
// and ACP run in the current project roots. Both preparation and the live-session
// fingerprint use this resolver, so changing roots reopens the idle runtime.
func (host *SessionHost) resolveSessionExecution(ctx context.Context, store workspace.Store, configured ...SessionOptions) (string, SessionWorkspaceContext, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	project, managed, err := coordinator.ManagerWorkspace(ctx, host.store.Dir, store)
	if err != nil {
		return "", SessionWorkspaceContext{}, false, err
	}
	if managed {
		root := project.WorkspaceRoots[0]
		info, err := os.Stat(root)
		if err != nil {
			return "", SessionWorkspaceContext{}, false, fmt.Errorf("manager execution directory is unavailable: %w", err)
		}
		if !info.IsDir() {
			return "", SessionWorkspaceContext{}, false, fmt.Errorf("manager execution directory %q is not a directory", root)
		}
		if err := workspace.RejectHomeDirectory(root); err != nil {
			return "", SessionWorkspaceContext{}, false, err
		}
		for _, checkout := range project.WorkspaceRoots {
			if err := (workspace.Store{Root: checkout}).EnsureQGitLocallyIgnored(ctx); err != nil {
				return "", SessionWorkspaceContext{}, false, err
			}
		}
		return root, SessionWorkspaceContext{ProjectID: project.ID, ProjectName: project.Name, AuxiliaryRoots: append([]string(nil), project.WorkspaceRoots[1:]...), Role: coordinator.SessionRole(project, store.SessionID)}, true, nil
	}
	root := store.Root
	if len(configured) > 0 && configured[0].WorkingDirectory != "" {
		root = configured[0].WorkingDirectory
	}
	resolved, found, err := host.resolveSessionWorkspace(root)
	if len(configured) > 0 && configured[0].WorkingDirectory != "" {
		resolved.AuxiliaryRoots = append([]string(nil), configured[0].AuxiliaryDirectories...)
	}
	return root, resolved, found, err
}
