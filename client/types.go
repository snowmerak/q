// Package client provides a configured OpenAI-compatible API client backed by
// github.com/snowmerak/llm-provider/providers/openai.
package client

import (
	llmprovider "github.com/snowmerak/llm-provider"
	provideropenai "github.com/snowmerak/llm-provider/providers/openai"
)

// Aliases keep agent code on one import path while retaining llm-provider's
// wire-compatible request and response types.
type (
	Role                  = llmprovider.Role
	Message               = llmprovider.Message
	MessageContentPart    = llmprovider.MessageContentPart
	ToolType              = llmprovider.ToolType
	FunctionDefinition    = llmprovider.FunctionDefinition
	Tool                  = llmprovider.Tool
	FunctionCall          = llmprovider.FunctionCall
	ToolCall              = llmprovider.ToolCall
	ToolChoiceMode        = llmprovider.ToolChoiceMode
	ToolResult            = llmprovider.ToolResult
	ToolHandler           = llmprovider.ToolHandler
	ChatRequest           = llmprovider.ChatRequest
	ChatResponse          = llmprovider.ChatResponse
	Choice                = llmprovider.Choice
	Usage                 = llmprovider.Usage
	TokenDetails          = llmprovider.TokenDetails
	ChatChunk             = llmprovider.ChatChunk
	Model                 = llmprovider.Model
	Stream                = llmprovider.Stream
	ResponseHeaderer      = llmprovider.ResponseHeaderer
	EmbeddingRequest      = llmprovider.EmbeddingRequest
	Embedding             = llmprovider.Embedding
	EmbeddingUsage        = llmprovider.EmbeddingUsage
	EmbeddingResponse     = llmprovider.EmbeddingResponse
	ModelCapabilities     = llmprovider.ModelCapabilities
	ReasoningControl      = llmprovider.ReasoningControl
	ReasoningCapabilities = llmprovider.ReasoningCapabilities
	RawResponse           = llmprovider.RawResponse
	ResponseEvent         = llmprovider.ResponseEvent
	ResponseStream        = llmprovider.ResponseStream
	APIError              = provideropenai.APIError
)

const (
	RoleSystem    = llmprovider.RoleSystem
	RoleDeveloper = llmprovider.RoleDeveloper
	RoleUser      = llmprovider.RoleUser
	RoleAssistant = llmprovider.RoleAssistant
	RoleTool      = llmprovider.RoleTool

	ToolTypeFunction = llmprovider.ToolTypeFunction

	ToolChoiceNone     = llmprovider.ToolChoiceNone
	ToolChoiceAuto     = llmprovider.ToolChoiceAuto
	ToolChoiceRequired = llmprovider.ToolChoiceRequired

	ReasoningControlEffort      = llmprovider.ReasoningControlEffort
	ReasoningControlToggle      = llmprovider.ReasoningControlToggle
	ReasoningControlTokenBudget = llmprovider.ReasoningControlTokenBudget
	ReasoningControlFixed       = llmprovider.ReasoningControlFixed
)

// NamedToolChoice forces a specific function tool in a chat request.
func NamedToolChoice(name string) map[string]any {
	return llmprovider.NamedToolChoice(name)
}
