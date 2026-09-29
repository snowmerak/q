package lsp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Service is the read-only LSP surface exposed through q's builtin tools.
// Manager implements it for one workspace; Router preserves that behavior
// while selecting a manager for each configured Studio project workspace.
type Service interface {
	Status(context.Context) StatusResult
	Diagnostics(context.Context, DiagnosticsRequest) (DiagnosticsResult, error)
	Hover(context.Context, PositionRequest) (*HoverResult, error)
	Definition(context.Context, PositionRequest) ([]LocationResult, error)
	References(context.Context, ReferencesRequest) ([]LocationResult, error)
	DocumentSymbols(context.Context, FileRequest) ([]SymbolResult, error)
	WorkspaceSymbols(context.Context, WorkspaceSymbolsRequest) ([]SymbolResult, error)
	Close() error
}

// Router combines independently configured workspace managers. Relative paths
// keep their existing meaning under the primary manager; additional workspaces
// are selected with absolute paths.
type Router struct {
	primary  *Manager
	managers []*Manager
}

func NewRouter(primary *Manager, additional ...*Manager) *Router {
	managers := make([]*Manager, 0, 1+len(additional))
	if primary != nil {
		managers = append(managers, primary)
	}
	for _, manager := range additional {
		if manager != nil {
			managers = append(managers, manager)
		}
	}
	return &Router{primary: primary, managers: managers}
}

func (r *Router) Status(ctx context.Context) StatusResult {
	if r == nil || r.primary == nil {
		return StatusResult{}
	}
	result := StatusResult{Workspace: r.primary.root}
	for _, manager := range r.managers {
		status := manager.Status(ctx)
		if manager != r.primary {
			for index := range status.Sessions {
				status.Sessions[index].Root = filepath.Clean(filepath.Join(manager.root, filepath.FromSlash(status.Sessions[index].Root)))
			}
		}
		result.Sessions = append(result.Sessions, status.Sessions...)
	}
	sort.Slice(result.Sessions, func(i, j int) bool {
		if result.Sessions[i].Root == result.Sessions[j].Root {
			return result.Sessions[i].Language < result.Sessions[j].Language
		}
		return result.Sessions[i].Root < result.Sessions[j].Root
	})
	return result
}

func (r *Router) Diagnostics(ctx context.Context, request DiagnosticsRequest) (DiagnosticsResult, error) {
	manager, err := r.managerForPath(request.Path)
	if err != nil {
		return DiagnosticsResult{}, err
	}
	result, err := manager.Diagnostics(ctx, request)
	if err == nil && manager != r.primary {
		result.Path = routedPath(manager.root, result.Path)
		for index := range result.Diagnostics {
			result.Diagnostics[index].Path = routedPath(manager.root, result.Diagnostics[index].Path)
		}
	}
	return result, err
}

func (r *Router) Hover(ctx context.Context, request PositionRequest) (*HoverResult, error) {
	manager, err := r.managerForPath(request.Path)
	if err != nil {
		return nil, err
	}
	return manager.Hover(ctx, request)
}

func (r *Router) Definition(ctx context.Context, request PositionRequest) ([]LocationResult, error) {
	manager, err := r.managerForPath(request.Path)
	if err != nil {
		return nil, err
	}
	result, err := manager.Definition(ctx, request)
	return r.rewriteLocations(manager, result), err
}

func (r *Router) References(ctx context.Context, request ReferencesRequest) ([]LocationResult, error) {
	manager, err := r.managerForPath(request.Path)
	if err != nil {
		return nil, err
	}
	result, err := manager.References(ctx, request)
	return r.rewriteLocations(manager, result), err
}

func (r *Router) DocumentSymbols(ctx context.Context, request FileRequest) ([]SymbolResult, error) {
	manager, err := r.managerForPath(request.Path)
	if err != nil {
		return nil, err
	}
	result, err := manager.DocumentSymbols(ctx, request)
	return r.rewriteSymbols(manager, result), err
}

func (r *Router) WorkspaceSymbols(ctx context.Context, request WorkspaceSymbolsRequest) ([]SymbolResult, error) {
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" {
		return nil, errors.New("lsp: workspace symbol query is required")
	}
	if request.Limit < 0 || request.Limit > 500 {
		return nil, errors.New("lsp: workspace symbol limit must be between 1 and 500")
	}
	if strings.TrimSpace(request.Path) != "" {
		manager, err := r.managerForPath(request.Path)
		if err != nil {
			return nil, err
		}
		result, err := manager.WorkspaceSymbols(ctx, request)
		return r.rewriteSymbols(manager, result), err
	}
	if r == nil || len(r.managers) == 0 {
		return nil, errors.New("lsp: no workspace managers")
	}
	type outcome struct {
		manager *Manager
		result  []SymbolResult
		err     error
	}
	outcomes := make(chan outcome, len(r.managers))
	var wait sync.WaitGroup
	for _, manager := range r.managers {
		wait.Go(func() {
			result, err := manager.WorkspaceSymbols(ctx, request)
			outcomes <- outcome{manager: manager, result: result, err: err}
		})
	}
	wait.Wait()
	close(outcomes)
	seen := make(map[string]struct{})
	var result []SymbolResult
	var resultErr error
	for outcome := range outcomes {
		if outcome.err != nil {
			resultErr = errors.Join(resultErr, outcome.err)
			continue
		}
		for _, symbol := range r.rewriteSymbols(outcome.manager, outcome.result) {
			key := symbol.Name + "\x00" + symbol.Location.Path + "\x00" + fmt.Sprint(symbol.Location.Range.Start.Line, ":", symbol.Location.Range.Start.Column)
			if _, found := seen[key]; found {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, symbol)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].Location.Path < result[j].Location.Path
		}
		return result[i].Name < result[j].Name
	})
	limit := request.Limit
	if limit == 0 {
		limit = 100
	}
	if len(result) > limit {
		result = result[:limit]
	}
	if len(result) == 0 && resultErr != nil {
		return nil, resultErr
	}
	return result, nil
}

func (r *Router) Close() error {
	if r == nil {
		return nil
	}
	var closeErr error
	for _, manager := range r.managers {
		closeErr = errors.Join(closeErr, manager.Close())
	}
	return closeErr
}

func (r *Router) managerForPath(path string) (*Manager, error) {
	if r == nil || r.primary == nil {
		return nil, errors.New("lsp: workspace manager is unavailable")
	}
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return r.primary, nil
	}
	candidate := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
		candidate = resolved
	}
	var selected *Manager
	for _, manager := range r.managers {
		if pathInside(manager.root, candidate) && (selected == nil || len(manager.root) > len(selected.root)) {
			selected = manager
		}
	}
	if selected == nil {
		return nil, errors.New("lsp: path escapes configured workspaces")
	}
	return selected, nil
}

func (r *Router) rewriteLocations(manager *Manager, result []LocationResult) []LocationResult {
	if manager == nil || manager == r.primary {
		return result
	}
	for index := range result {
		if !result[index].External {
			result[index].Path = routedPath(manager.root, result[index].Path)
		}
	}
	return result
}

func (r *Router) rewriteSymbols(manager *Manager, result []SymbolResult) []SymbolResult {
	if manager == nil || manager == r.primary {
		return result
	}
	for index := range result {
		if !result[index].Location.External {
			result[index].Location.Path = routedPath(manager.root, result[index].Location.Path)
		}
	}
	return result
}

func routedPath(root, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(root, filepath.FromSlash(path)))
}
