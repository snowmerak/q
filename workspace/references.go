package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/snowmerak/q/loom"
)

// LoomReferencesAt returns Loom references present in the current workspace
// session projection and active plan checkpoint. Durable archive records are
// intentionally not roots.
func LoomReferencesAt(ctx context.Context, root string) ([]loom.Ref, error) {
	if ctx == nil {
		return nil, errors.New("workspace: Loom reference context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store := Store{Root: root}
	values := make([]string, 0, 2)
	session, err := store.Load()
	if err == nil {
		body, marshalErr := json.Marshal(session)
		if marshalErr != nil {
			return nil, fmt.Errorf("workspace: encode session for Loom references: %w", marshalErr)
		}
		values = append(values, string(body))
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	execution, err := store.LoadExecution()
	if err == nil {
		body, marshalErr := json.Marshal(execution)
		if marshalErr != nil {
			return nil, fmt.Errorf("workspace: encode plan execution for Loom references: %w", marshalErr)
		}
		values = append(values, string(body))
	} else if !errors.Is(err, ErrExecutionNotFound) {
		return nil, err
	}
	return loom.ExtractReferences(values...), nil
}
