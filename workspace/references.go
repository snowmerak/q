package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/snowmerak/q/loom"
)

// LoomReferencesAt returns Loom references present in every workspace session
// projection and active plan checkpoint. Durable archive records are
// intentionally not roots.
func LoomReferencesAt(ctx context.Context, root string) ([]loom.Ref, error) {
	if ctx == nil {
		return nil, errors.New("workspace: Loom reference context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	base := Store{Root: root}
	entries, err := base.ListSessions()
	if err != nil {
		return nil, err
	}
	stores := make([]Store, 0, len(entries)+1)
	for _, entry := range entries {
		stores = append(stores, entry.Store)
	}
	// Include pre-migration projections so standalone q-mcp retains their Loom
	// roots even before a TUI or ACP host performs the migration.
	stores = append(stores, base)
	references := make(map[loom.Ref]struct{})
	collect := func(value any, label string) error {
		body, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("workspace: encode %s for Loom references: %w", label, err)
		}
		for _, ref := range loom.ExtractReferences(string(body)) {
			references[ref] = struct{}{}
		}
		return nil
	}
	for _, store := range stores {
		session, loadErr := store.Load()
		if loadErr == nil {
			if err := collect(session, "session"); err != nil {
				return nil, err
			}
		} else if !errors.Is(loadErr, ErrNotFound) {
			return nil, loadErr
		}
		execution, executionErr := store.LoadExecution()
		if executionErr == nil {
			if err := collect(execution, "plan execution"); err != nil {
				return nil, err
			}
		} else if !errors.Is(executionErr, ErrExecutionNotFound) {
			return nil, executionErr
		}
	}
	result := make([]loom.Ref, 0, len(references))
	for ref := range references {
		result = append(result, ref)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}
