package task

import (
	"context"
	"fmt"
	"sort"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
	"github.com/snowmerak/q/v2/pkg/memory"
	"github.com/snowmerak/q/v2/pkg/storage"
)

type RegistryDeps struct {
	RDB        storage.RDBStore
	Search     storage.SearchIndex
	Loom       *LoomStore
	Commands   *CommandManager
	Skills     *SkillStore
	MCP        *MCPManager
	Browser    *BrowserController
	EmbedderFn func() memory.QueryEmbedder
}

// Registry owns all built-in tools + external MCP tool routes and automatically
// spills large tool outputs into LoomStore.
type Registry struct {
	builtins map[string]ToolSpec
	loom     *LoomStore
	mcp      *MCPManager
}

func NewRegistry(deps RegistryDeps) *Registry {
	reg := &Registry{
		builtins: make(map[string]ToolSpec),
		loom:     deps.Loom,
		mcp:      deps.MCP,
	}
	registerFileTools(reg)
	registerShellTools(reg, deps.Commands)
	registerArchiveAndSkillTools(reg, deps.RDB, deps.Search, deps.EmbedderFn, deps.Skills)
	registerLoomTools(reg, deps.Loom)
	registerBrowserTool(reg, deps.Browser)
	registerOrchestrationTools(reg)
	return reg
}

func (r *Registry) register(spec ToolSpec) {
	r.builtins[spec.Name] = spec
}

func (r *Registry) Lookup(name string) (ToolSpec, bool) {
	if r == nil {
		return ToolSpec{}, false
	}
	if spec, ok := r.builtins[name]; ok {
		return spec, true
	}
	if r.mcp != nil {
		for _, spec := range r.mcp.Tools() {
			if spec.Name == name {
				return spec, true
			}
		}
	}
	return ToolSpec{}, false
}

func (r *Registry) Specs() []ToolSpec {
	if r == nil {
		return nil
	}
	out := make([]ToolSpec, 0, len(r.builtins))
	for _, spec := range r.builtins {
		out = append(out, spec)
	}
	if r.mcp != nil {
		out = append(out, r.mcp.Tools()...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ToolDefinitions returns OpenAI-compatible function tool descriptors for LLM requests.
func (r *Registry) ToolDefinitions() []llmprovider.Tool {
	specs := r.Specs()
	defs := make([]llmprovider.Tool, 0, len(specs))
	for _, spec := range specs {
		defs = append(defs, llmprovider.Tool{
			Type: llmprovider.ToolTypeFunction,
			Function: llmprovider.FunctionDefinition{
				Name:        spec.Name,
				Description: spec.Description,
				Parameters:  spec.Parameters,
			},
		})
	}
	return defs
}

// Execute runs the named tool and automatically spills outputs exceeding 16 KiB
// into LoomStore unless the tool itself reads/inspects Loom artifacts.
func (r *Registry) Execute(ctx context.Context, call CallContext, name, argsJSON string) (ToolOutput, error) {
	spec, ok := r.Lookup(name)
	if !ok {
		return ToolOutput{Content: fmt.Sprintf("unknown tool %q", name), IsError: true}, nil
	}
	out, err := spec.Handler(ctx, call, argsJSON)
	if err != nil {
		return ToolOutput{Content: err.Error(), IsError: true}, nil
	}
	if out.PauseKind == "" && out.ArtifactID == "" && r.loom != nil && name != "loom_read" && name != "loom_inspect" && name != "loom_eval" {
		inline, artID, spilled, spillErr := r.loom.SpillIfLarge(ctx, out.Content, "text/plain; charset=utf-8", map[string]any{
			"tool":    name,
			"room_id": call.RoomID,
			"turn_id": call.TurnID,
		})
		if spillErr == nil && spilled {
			out.Content = inline
			out.ArtifactID = artID
		}
	}
	return out, nil
}
