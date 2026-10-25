package composer

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workspace"
)

type SessionRunner interface {
	RunWithOptions(context.Context, workspace.Store, string, string, app.SessionOptions, app.SessionEventSink) error
	ReleaseSession(workspace.Store, string) error
}

const instructions = `You are Composer, a conversational partner helping the user develop ideas.
Clarify goals, audience, constraints, assumptions, and tradeoffs through conversation. Offer concrete alternatives and examples; distinguish agreed decisions from open questions. Match the user's language and pace. Use ask_to_user when a focused question helps the user choose a direction, then continue from their answer.
Develop visual ideas together using D2 diagrams and interactive HTML components. Your artifact tools can list, read, create, replace, and delete UTF-8 source files only in docs/diagram/*.d2 and docs/webcomponent/*.html, including subdirectories. These paths belong to this private Composer session. Write or revise files when the user requests them, and explain which preview to open. Read existing source before editing; use expected_hash to avoid overwriting concurrent changes. Preserve the user's work and refine it from their feedback.
D2 previews use the dagre layout. D2 imports can reference only other diagrams in docs/diagram. HTML previews allow inline CSS and JavaScript in an isolated sandbox, with no network, parent-page access, external scripts, or external assets. Make components self-contained. A rendering error does not delete the source: correct the source and try again.
Use skills, archived conversations, and propositions when relevant. Use learn to retain reusable knowledge through Q's existing learning pipeline. You have no command execution, general filesystem, delegation, or external MCP tools. Do not claim to modify application code or files outside the artifact directories.`

var allowedTools = []string{
	"ask_to_user", "search_skills", "get_skill", "search_archive", "get_archive_record",
	"search_propositions", "get_proposition", "learn",
	"composer_list_artifacts", "composer_read_artifact", "composer_write_artifact", "composer_delete_artifact",
}

// Options retains Q's standard archive, Skill Library, learning, and Agent Loop.
// The host enforces the allowlist for ordinary and loop-owned tool calls alike.
func Options(model, operation string) app.SessionOptions {
	return app.SessionOptions{
		Model: model, SystemPrompt: instructions, RuntimeKey: "composer-artifacts-v1", OperationID: operation,
		AllowedTools:       slices.Clone(allowedTools),
		DisableExternalMCP: true,
		WrapRuntime: func(_ context.Context, store workspace.Store, base app.AgentToolRuntime) (app.AgentToolRuntime, error) {
			return &artifactRuntime{base: base, files: &Artifacts{Root: store.Root}}, nil
		},
	}
}

type artifactRuntime struct {
	base  app.AgentToolRuntime
	files *Artifacts
}

func (r *artifactRuntime) Environment() tools.HostEnvironment { return r.base.Environment() }
func (r *artifactRuntime) SearchSkillHints(ctx context.Context, query string, limit int) (tools.SkillHintSearchResult, error) {
	if searcher, ok := r.base.(interface {
		SearchSkillHints(context.Context, string, int) (tools.SkillHintSearchResult, error)
	}); ok {
		return searcher.SearchSkillHints(ctx, query, limit)
	}
	return tools.SkillHintSearchResult{}, nil
}

func (r *artifactRuntime) Tools() []client.Tool {
	result := []client.Tool{}
	for _, tool := range r.base.Tools() {
		if slices.Contains(allowedTools, tool.Function.Name) {
			result = append(result, tool)
		}
	}
	for _, spec := range []struct {
		name, description string
		required          []string
	}{
		{"composer_list_artifacts", "List the D2 and HTML source files in this Composer.", nil},
		{"composer_read_artifact", "Read one managed artifact's UTF-8 source and revision hash.", []string{"path"}},
		{"composer_write_artifact", "Create or replace one D2 or HTML source. Optional expected_hash checks the prior revision; empty means create only.", []string{"path", "content"}},
		{"composer_delete_artifact", "Delete one managed artifact. Optional expected_hash checks the prior revision.", []string{"path"}},
	} {
		properties := map[string]any{}
		if spec.name != "composer_list_artifacts" {
			properties["path"] = map[string]any{"type": "string", "description": "Relative docs/diagram/name.d2 or docs/webcomponent/name.html path"}
		}
		if spec.name == "composer_write_artifact" {
			properties["content"] = map[string]any{"type": "string"}
		}
		if spec.name == "composer_write_artifact" || spec.name == "composer_delete_artifact" {
			properties["expected_hash"] = map[string]any{"type": "string"}
		}
		schema := map[string]any{"type": "object", "properties": properties, "required": append([]string{}, spec.required...), "additionalProperties": false}
		result = append(result, client.Tool{Type: "function", Function: client.FunctionDefinition{Name: spec.name, Description: spec.description, Parameters: schema}})
	}
	return result
}

func (r *artifactRuntime) Call(ctx context.Context, call client.ToolCall) (client.ToolResult, error) {
	if !slices.Contains(allowedTools, call.Function.Name) {
		return client.ToolResult{}, fmt.Errorf("tool %q is not allowed in Composer", call.Function.Name)
	}
	var input struct {
		Path         string  `json:"path"`
		Content      string  `json:"content"`
		ExpectedHash *string `json:"expected_hash"`
	}
	var value any
	var err error
	switch call.Function.Name {
	case "composer_list_artifacts":
		value, err = r.files.List()
	case "composer_read_artifact", "composer_write_artifact", "composer_delete_artifact":
		if err = json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
			return client.ToolResult{}, err
		}
		switch call.Function.Name {
		case "composer_read_artifact":
			value, err = r.files.Read(input.Path)
		case "composer_write_artifact":
			value, err = r.files.Write(input.Path, input.Content, input.ExpectedHash)
		case "composer_delete_artifact":
			err = r.files.Delete(input.Path, input.ExpectedHash)
			value = map[string]bool{"deleted": err == nil}
		}
	default:
		return r.base.Call(ctx, call)
	}
	if err != nil {
		return client.ToolResult{}, err
	}
	body, err := json.Marshal(value)
	return client.ToolResult{Content: string(body)}, err
}
