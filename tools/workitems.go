package tools

import (
	"github.com/snowmerak/q/internal/workitemtools"
	"github.com/snowmerak/q/library"
)

// WithWorkspaceWork binds source mutations to the host-selected project and
// actor. Tool arguments cannot choose a different workspace or manager role.
func WithWorkspaceWork(client *library.Client, workspaceID, sessionID string, manager bool) RuntimeOption {
	return func(options *runtimeOptions) {
		options.workClient = client
		options.workBinding = workitemtools.Binding{WorkspaceID: workspaceID, SessionID: sessionID, Manager: manager}
	}
}
