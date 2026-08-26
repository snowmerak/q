package workspace

import (
	"context"

	"github.com/snowmerak/q/loom"
)

// withLoomRootMutation keeps a projection update atomic with Loom root scans
// and garbage collection. Session locks protect ownership of one projection;
// this lease protects the shared artifact graph across all sessions.
func withLoomRootMutation(root string, operation func() error) error {
	return loom.WithWorkspaceOperation(context.Background(), root, func(context.Context) error {
		return operation()
	})
}
