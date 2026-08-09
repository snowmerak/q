package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/loom"
)

const (
	maximumInlineTargetRead = 1 << 20
	maximumTargetBytes      = 8 << 20
)

// TargetResolver evaluates every selector independently, intersects the
// selectors in each All product, then unions all Any products.
type TargetResolver struct {
	Tools ToolRuntime
}

func (r TargetResolver) Resolve(ctx context.Context, target TargetCondition) ([]string, error) {
	if ctx == nil {
		return nil, errors.New("subagent: target resolver context is nil")
	}
	if err := validateTargetCondition(&target); err != nil {
		return nil, err
	}
	resolved := make(map[string]struct{})
	for groupIndex, group := range target.Any {
		var product map[string]struct{}
		for selectorIndex, selector := range group.All {
			paths, err := r.resolveSelector(ctx, selector, groupIndex, selectorIndex)
			if err != nil {
				return nil, err
			}
			current := stringSet(paths)
			if product == nil {
				product = current
			} else {
				product = intersectSets(product, current)
			}
		}
		for path := range product {
			resolved[path] = struct{}{}
		}
	}
	paths := sortedSet(resolved)
	if len(paths) == 0 {
		return nil, errors.New("subagent: target condition resolved no files")
	}
	return paths, nil
}

func (r TargetResolver) resolveSelector(ctx context.Context, selector TargetSelector, groupIndex, selectorIndex int) ([]string, error) {
	if selector.Kind == TargetSelectorPaths {
		return normalizeResolvedPaths(selector.Paths)
	}
	if r.Tools == nil {
		return nil, errors.New("subagent: target resolver tool runtime is required for Loom selectors")
	}
	arguments, err := json.Marshal(map[string]any{"code": selector.Code, "inputs": selector.Inputs})
	if err != nil {
		return nil, err
	}
	result, err := r.Tools.Call(ctx, client.ToolCall{
		ID: fmt.Sprintf("target-%d-%d", groupIndex+1, selectorIndex+1), Type: client.ToolTypeFunction,
		Function: client.FunctionCall{Name: "loom_eval", Arguments: string(arguments)},
	})
	if err != nil {
		return nil, fmt.Errorf("subagent: target Loom selector %d.%d: %w", groupIndex+1, selectorIndex+1, err)
	}
	if result.IsError {
		return nil, fmt.Errorf("subagent: target Loom selector %d.%d: %s", groupIndex+1, selectorIndex+1, strings.TrimSpace(result.Content))
	}
	var output struct {
		Artifact loom.Artifact   `json:"artifact"`
		Value    json.RawMessage `json:"value,omitempty"`
	}
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		return nil, fmt.Errorf("subagent: decode target Loom selector %d.%d: %w", groupIndex+1, selectorIndex+1, err)
	}
	body := output.Value
	if len(body) == 0 {
		if output.Artifact.Ref == "" {
			return nil, fmt.Errorf("subagent: target Loom selector %d.%d returned neither a value nor an artifact", groupIndex+1, selectorIndex+1)
		}
		body, err = r.readArtifact(ctx, output.Artifact.Ref)
		if err != nil {
			return nil, fmt.Errorf("subagent: read target Loom selector %d.%d: %w", groupIndex+1, selectorIndex+1, err)
		}
	}
	var paths []string
	if err := json.Unmarshal(body, &paths); err != nil {
		return nil, fmt.Errorf("subagent: target Loom selector %d.%d must return a JSON string array: %w", groupIndex+1, selectorIndex+1, err)
	}
	return normalizeResolvedPaths(paths)
}

func (r TargetResolver) readArtifact(ctx context.Context, ref loom.Ref) ([]byte, error) {
	var body strings.Builder
	var offset int64
	for body.Len() <= maximumTargetBytes {
		arguments, err := json.Marshal(map[string]any{
			"ref": ref.String(), "offset": offset, "limit": maximumInlineTargetRead,
		})
		if err != nil {
			return nil, err
		}
		result, err := r.Tools.Call(ctx, client.ToolCall{
			ID: fmt.Sprintf("target-read-%d", offset), Type: client.ToolTypeFunction,
			Function: client.FunctionCall{Name: "loom_read", Arguments: string(arguments)},
		})
		if err != nil {
			return nil, err
		}
		if result.IsError {
			return nil, errors.New(strings.TrimSpace(result.Content))
		}
		var read struct {
			Content    string `json:"content"`
			Encoding   string `json:"encoding"`
			NextOffset int64  `json:"next_offset"`
			More       bool   `json:"more"`
		}
		if err := json.Unmarshal([]byte(result.Content), &read); err != nil {
			return nil, err
		}
		if read.Encoding != "utf-8" {
			return nil, fmt.Errorf("target artifact encoding %q is not supported", read.Encoding)
		}
		body.WriteString(read.Content)
		if body.Len() > maximumTargetBytes {
			return nil, fmt.Errorf("target artifact exceeds %d bytes", maximumTargetBytes)
		}
		if !read.More {
			return []byte(body.String()), nil
		}
		if read.NextOffset <= offset {
			return nil, errors.New("target artifact read did not advance")
		}
		offset = read.NextOffset
	}
	return nil, fmt.Errorf("target artifact exceeds %d bytes", maximumTargetBytes)
}

func normalizeResolvedPaths(paths []string) ([]string, error) {
	result := make(map[string]struct{}, len(paths))
	for _, path := range cleanStrings(paths) {
		if !workspaceRelativePath(path) {
			return nil, fmt.Errorf("resolved target path %q must stay workspace-relative", path)
		}
		result[filepath.ToSlash(filepath.Clean(path))] = struct{}{}
	}
	return sortedSet(result), nil
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func intersectSets(left, right map[string]struct{}) map[string]struct{} {
	if len(left) > len(right) {
		left, right = right, left
	}
	result := make(map[string]struct{}, len(left))
	for value := range left {
		if _, exists := right[value]; exists {
			result[value] = struct{}{}
		}
	}
	return result
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
