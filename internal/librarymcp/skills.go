package librarymcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/library"
)

const SkillInstructions = "Q supplies global and bundled Agent Skills through the q-library MCP server. Call search_skills with the skill name or relevant keywords, then call get_skill with a returned ID before using its instructions. Reuse complete skill text already available in the current conversation."

type skillSearchInput struct {
	Query string   `json:"query" jsonschema:"Skill name or concise keywords for the procedure needed"`
	Tags  []string `json:"tags,omitempty" jsonschema:"Optional tags; a skill must match at least one"`
	Limit int      `json:"limit,omitempty" jsonschema:"Maximum results, from 1 to 100; defaults to 20"`
}

type skillInput struct {
	ID   string `json:"id" jsonschema:"Exact skill ID returned by search_skills"`
	Path string `json:"path,omitempty" jsonschema:"Optional skill-relative resource path; defaults to SKILL.md"`
}

type skillOutput struct {
	Skill   agentskills.Skill `json:"skill"`
	Path    string            `json:"path"`
	Content string            `json:"content"`
}

func registerSkillTools(server *mcp.Server, client *library.Client) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	mcp.AddTool(server, &mcp.Tool{
		Name: "search_skills", Description: "Search global and bundled Q skills. Use get_skill with a result's ID to read the instructions.", Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in skillSearchInput) (*mcp.CallToolResult, library.SkillSearchResponse, error) {
		out, err := client.SearchSkills(ctx, library.SkillSearchRequest{Query: in.Query, Tags: in.Tags, Limit: in.Limit})
		return nil, out, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_skill", Description: "Read the full SKILL.md or a skill-relative text resource by the exact skill ID.", Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in skillInput) (*mcp.CallToolResult, skillOutput, error) {
		out, err := client.GetSkill(ctx, in.ID, in.Path)
		if err != nil {
			return nil, skillOutput{}, err
		}
		return nil, skillOutput{Skill: out.Skill, Path: out.Path, Content: string(out.Content)}, nil
	})
}
