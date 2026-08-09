package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/snowmerak/q/loom"
)

// LoomReferencesAt returns Loom references present in the current workspace
// session projection. Durable archive records are intentionally not roots.
func LoomReferencesAt(ctx context.Context, root string) ([]loom.Ref, error) {
	if ctx == nil {
		return nil, errors.New("workspace: Loom reference context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	session, err := (Store{Root: root}).Load()
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(session)
	if err != nil {
		return nil, fmt.Errorf("workspace: encode session for Loom references: %w", err)
	}
	return loom.ExtractReferences(string(body)), nil
}
