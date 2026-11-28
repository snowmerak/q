package llmprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// Role identifies the author of a chat message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is the common text-message subset supported by all providers.
type Message struct {
	Role Role `json:"role"`
	// Phase distinguishes intermediate commentary from a final answer when
	// reconstructing Responses history. Chat wire messages omit this field.
	Phase string `json:"-"`
	// Content is the common text form. ContentParts preserves structured
	// OpenAI-compatible content blocks, including provider cache breakpoints.
	// When ContentParts is non-empty it is encoded as the content field instead
	// of Content.
	Content      string               `json:"-"`
	ContentParts []MessageContentPart `json:"-"`
	// Reasoning stores provider-supplied thinking/reasoning text on incoming
	// responses/chunks. Chat wire serialization intentionally omits it.
	Reasoning    string               `json:"-"`
	Name         string               `json:"name,omitempty"`
	ToolCallID   string               `json:"tool_call_id,omitempty"`
	ToolCalls    []ToolCall           `json:"tool_calls,omitempty"`
	// ResponseOutput retains native Responses items for stateless replay. Chat
	// requests and user-facing message archives never serialize these items.
	ResponseOutput []json.RawMessage `json:"-"`
	// ResponseModel identifies the model that produced ResponseOutput. Native
	// output must only be replayed to the same model.
	ResponseModel string `json:"-"`
}

// MessageContentPart retains provider-specific multimodal and cache-control
// fields without narrowing the OpenAI-compatible content block schema.
type MessageContentPart map[string]any

func (m Message) MarshalJSON() ([]byte, error) {
	type wireMessage struct {
		Role       Role       `json:"role"`
		Content    any        `json:"content,omitempty"`
		Name       string     `json:"name,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	}
	var content any
	if len(m.ContentParts) > 0 {
		content = m.ContentParts
	} else if m.Content != "" {
		content = m.Content
	}
	return json.Marshal(wireMessage{
		Role: m.Role, Content: content, Name: m.Name,
		ToolCallID: m.ToolCallID, ToolCalls: m.ToolCalls,
	})
}

func (m *Message) UnmarshalJSON(data []byte) error {
	type wireMessage struct {
		Role             Role            `json:"role"`
		Content          json.RawMessage `json:"content"`
		ReasoningContent string          `json:"reasoning_content,omitempty"`
		Reasoning        string          `json:"reasoning,omitempty"`
		Name             string          `json:"name,omitempty"`
		ToolCallID       string          `json:"tool_call_id,omitempty"`
		ToolCalls        []ToolCall      `json:"tool_calls,omitempty"`
	}
	var wire wireMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	reasoning := wire.ReasoningContent
	if reasoning == "" {
		reasoning = wire.Reasoning
	}
	m.Role, m.Phase, m.Name, m.ToolCallID, m.ToolCalls = wire.Role, "", wire.Name, wire.ToolCallID, wire.ToolCalls
	m.Content, m.ContentParts, m.Reasoning = "", nil, reasoning
	if len(wire.Content) == 0 || string(wire.Content) == "null" {
		return nil
	}
	if wire.Content[0] == '"' {
		return json.Unmarshal(wire.Content, &m.Content)
	}
	if err := json.Unmarshal(wire.Content, &m.ContentParts); err != nil {
		return fmt.Errorf("message content must be a string or content-block array: %w", err)
	}
	return nil
}

// TextContent returns the text represented by either Content or supported
// text content blocks. It is used by providers such as Codex whose protocol
// accepts text at the common chat boundary.
func (m Message) TextContent() string {
	if len(m.ContentParts) == 0 {
		return m.Content
	}
	parts := make([]string, 0, len(m.ContentParts))
	for _, part := range m.ContentParts {
		typeName, _ := part["type"].(string)
		if typeName != "text" && typeName != "input_text" && typeName != "output_text" {
			continue
		}
		if value, ok := part["text"].(string); ok {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, "")
}

type ToolType string

const ToolTypeFunction ToolType = "function"

// FunctionDefinition describes a function with JSON Schema parameters.
type FunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      *bool          `json:"strict,omitempty"`
}

type Tool struct {
	Type     ToolType           `json:"type"`
	Function FunctionDefinition `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall represents both a complete call and a streamed call fragment.
// Index identifies fragments belonging to the same streamed call.
type ToolCall struct {
	Index    int          `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     ToolType     `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

type ToolChoiceMode string

const (
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceRequired ToolChoiceMode = "required"
)

// ToolResult is returned by a Codex ToolHandler. Content may be plain text or
// JSON encoded as text. IsError tells the model that the call failed.
type ToolResult struct {
	Content string
	IsError bool
}

// ToolHandler executes a Codex dynamic function call in-process. When it is
// nil, the Codex provider delegates dynamic calls to the caller using the
// OpenAI tool_calls response shape.
type ToolHandler func(context.Context, ToolCall) (ToolResult, error)

// ChatRequest describes a chat completion. Extra fields supply provider-specific
// request fields for HTTP providers; typed fields take precedence where supported.
type ChatRequest struct {
	Model               string    `json:"model,omitempty"`
	Messages            []Message `json:"messages"`
	Temperature         *float64  `json:"temperature,omitempty"`
	TopP                *float64  `json:"top_p,omitempty"`
	MaxTokens           *int      `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int      `json:"max_completion_tokens,omitempty"`
	Stop                []string  `json:"stop,omitempty"`
	Tools               []Tool    `json:"tools,omitempty"`
	// ToolChoice accepts ToolChoiceMode or an OpenAI-compatible named-tool
	// object such as {"type":"function","function":{"name":"get_weather"}}.
	ToolChoice        any            `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool          `json:"parallel_tool_calls,omitempty"`
	Extra             map[string]any `json:"-"`
	// Headers are request-scoped transport headers. HTTP providers forward
	// them after applying provider defaults. Authorization, Content-Type, and
	// Accept remain provider-managed and cannot be overridden here.
	Headers http.Header `json:"-"`

	// ConversationID continues a stateful provider conversation. The
	// OpenAI-compatible provider forwards it as an extension field so gateways
	// can route stateful backends. The Codex provider treats it as a thread ID.
	ConversationID string `json:"conversation_id,omitempty"`
	// WorkingDirectory and OutputSchema are optional provider-specific turn
	// overrides. ReasoningEffort is forwarded using each provider's native
	// request shape when supported.
	WorkingDirectory string      `json:"-"`
	ReasoningEffort  string      `json:"reasoning_effort,omitempty"`
	OutputSchema     any         `json:"-"`
	ToolHandler      ToolHandler `json:"-"`
}

// NamedToolChoice returns an OpenAI-compatible choice that forces one
// function tool.
func NamedToolChoice(name string) map[string]any {
	return map[string]any{"type": "function", "function": map[string]string{"name": name}}
}

type ChatResponse struct {
	ID             string          `json:"id"`
	Object         string          `json:"object,omitempty"`
	Created        int64           `json:"created,omitempty"`
	Model          string          `json:"model,omitempty"`
	Choices        []Choice        `json:"choices"`
	Usage          Usage           `json:"usage,omitempty"`
	ConversationID string          `json:"conversation_id,omitempty"`
	Headers        http.Header     `json:"-"`
	Raw            json.RawMessage `json:"-"`
}

type Choice struct {
	Index        int      `json:"index"`
	Message      Message  `json:"message"`
	FinishReason string   `json:"finish_reason,omitempty"`
	Delta        *Message `json:"delta,omitempty"`
	// Phase is provider metadata. Codex uses "commentary" and
	// "final_answer"; OpenAI-compatible responses normally leave it empty.
	// It is serialized on streamed chunks so an HTTP gateway does not erase
	// the distinction before the downstream client consumes the stream.
	Phase string `json:"phase,omitempty"`
}

type Usage struct {
	PromptTokens     int           `json:"prompt_tokens,omitempty"`
	CompletionTokens int           `json:"completion_tokens,omitempty"`
	TotalTokens      int           `json:"total_tokens,omitempty"`
	PromptDetails    *TokenDetails `json:"prompt_tokens_details,omitempty"`
}

// TokenDetails normalizes prompt-cache accounting exposed by compatible
// providers while retaining the OpenAI field names on the wire.
type TokenDetails struct {
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// ChatChunk uses the OpenAI chat.completion.chunk shape and also carries the
// provider conversation ID when one exists.
type ChatChunk struct {
	ID             string          `json:"id"`
	Object         string          `json:"object,omitempty"`
	Created        int64           `json:"created,omitempty"`
	Model          string          `json:"model,omitempty"`
	Choices        []Choice        `json:"choices"`
	Usage          *Usage          `json:"usage,omitempty"`
	ConversationID string          `json:"conversation_id,omitempty"`
	Raw            json.RawMessage `json:"-"`
}

// Model is the common OpenAI-compatible model-list representation.
type Model struct {
	ID              string             `json:"id"`
	Object          string             `json:"object,omitempty"`
	Created         int64              `json:"created,omitempty"`
	OwnedBy         string             `json:"owned_by,omitempty"`
	ContextLength   int64              `json:"context_length,omitempty"`
	MaxOutputTokens int64              `json:"max_output_tokens,omitempty"`
	Capabilities    *ModelCapabilities `json:"capabilities,omitempty"`
}

// ModelMetadata contains optional model capabilities that can be discovered
// from a provider or overridden by Gateway configuration.
type ModelMetadata struct {
	ContextLength   int64              `json:"context_length,omitempty"`
	MaxOutputTokens int64              `json:"max_output_tokens,omitempty"`
	Capabilities    *ModelCapabilities `json:"capabilities,omitempty"`
}

// ModelCapabilities describes optional controls advertised for a model.
type ModelCapabilities struct {
	// ToolCalling is nil when the catalog does not report tool support.
	ToolCalling *bool                  `json:"tool_calling,omitempty"`
	Reasoning   *ReasoningCapabilities `json:"reasoning,omitempty"`
}

// UnmarshalJSON accepts both q's structured capability metadata and the flat
// capability-name lists exposed by some OpenAI-compatible local servers.
func (c *ModelCapabilities) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var advertised []string
		if err := json.Unmarshal(trimmed, &advertised); err != nil {
			return err
		}
		*c = ModelCapabilities{}
		for _, name := range advertised {
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "reasoning":
				c.Reasoning = &ReasoningCapabilities{
					Supported: true,
					Control:   ReasoningControlFixed,
				}
			case "tools", "tool calling", "tool_calling", "tool use", "tool_use", "function calling", "function_calling":
				supported := true
				c.ToolCalling = &supported
			}
		}
		return nil
	}

	type capabilities ModelCapabilities
	var decoded capabilities
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return err
	}
	*c = ModelCapabilities(decoded)
	return nil
}

type ReasoningControl string

const (
	ReasoningControlEffort      ReasoningControl = "effort"
	ReasoningControlToggle      ReasoningControl = "toggle"
	ReasoningControlTokenBudget ReasoningControl = "token_budget"
	ReasoningControlFixed       ReasoningControl = "fixed"
)

// ReasoningCapabilities describes how callers may control model reasoning.
// An effort control with an empty SupportedEfforts list accepts provider-
// defined effort values rather than a closed enumerated set.
type ReasoningCapabilities struct {
	Supported         bool             `json:"supported"`
	Control           ReasoningControl `json:"control,omitempty"`
	SupportedEfforts  []string         `json:"supported_efforts,omitempty"`
	DefaultEffort     string           `json:"default_effort,omitempty"`
	DefaultEnabled    *bool            `json:"default_enabled,omitempty"`
	Mandatory         bool             `json:"mandatory,omitempty"`
	SupportsMaxTokens bool             `json:"supports_max_tokens,omitempty"`
}

// UnmarshalJSON accepts common model metadata extensions exposed by
// OpenAI-compatible servers and normalizes them to the common output fields.
func (m *Model) UnmarshalJSON(data []byte) error {
	type model Model
	var decoded model
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*m = Model(decoded)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if m.ContextLength <= 0 {
		m.ContextLength = 0
		for _, name := range []string{"max_input_tokens", "max_model_len", "context_window", "context_window_tokens", "max_context_length"} {
			if value := positiveJSONInt(fields[name]); value > 0 {
				m.ContextLength = value
				break
			}
		}
	}
	if m.MaxOutputTokens <= 0 {
		m.MaxOutputTokens = 0
		for _, name := range []string{"max_tokens", "max_completion_tokens"} {
			if value := positiveJSONInt(fields[name]); value > 0 {
				m.MaxOutputTokens = value
				break
			}
		}
	}
	capabilities := m.Capabilities
	if capabilities == nil {
		capabilities = &ModelCapabilities{}
	}
	if capabilities.ToolCalling == nil {
		var params []string
		if data := fields["supported_parameters"]; len(data) > 0 && json.Unmarshal(data, &params) == nil && params != nil {
			supported := slices.Contains(params, "tools")
			capabilities.ToolCalling = &supported
		}
	}
	catalogCapabilities := bytes.TrimSpace(fields["capabilities"])
	if capabilities.Reasoning == nil || bytes.HasPrefix(catalogCapabilities, []byte("[")) {
		if reasoning := decodeCatalogReasoning(fields["reasoning"]); reasoning != nil {
			capabilities.Reasoning = reasoning
		}
	}
	if capabilities.ToolCalling != nil || capabilities.Reasoning != nil {
		m.Capabilities = capabilities
	}
	return nil
}

func decodeCatalogReasoning(data json.RawMessage) *ReasoningCapabilities {
	var catalog struct {
		ReasoningCapabilities
		Supported *bool    `json:"supported"`
		Efforts   []string `json:"efforts"`
	}
	if len(data) == 0 || json.Unmarshal(data, &catalog) != nil {
		return nil
	}
	if catalog.Supported != nil && !*catalog.Supported {
		return &ReasoningCapabilities{}
	}
	reasoning := catalog.ReasoningCapabilities
	if reasoning.SupportedEfforts == nil {
		reasoning.SupportedEfforts = catalog.Efforts
	}
	switch reasoning.Control {
	case ReasoningControlEffort, ReasoningControlToggle, ReasoningControlTokenBudget, ReasoningControlFixed:
	default:
		switch {
		case reasoning.SupportedEfforts != nil || reasoning.DefaultEffort != "":
			reasoning.Control = ReasoningControlEffort
		case reasoning.SupportsMaxTokens:
			reasoning.Control = ReasoningControlTokenBudget
		case reasoning.DefaultEnabled != nil:
			reasoning.Control = ReasoningControlToggle
		case catalog.Supported != nil || reasoning.Mandatory:
			reasoning.Control = ReasoningControlFixed
		default:
			return nil
		}
	}
	reasoning.Supported = true
	return &reasoning
}

func positiveJSONInt(data json.RawMessage) int64 {
	if len(data) == 0 {
		return 0
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err == nil {
		value, _ := number.Int64()
		return value
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		value, _ := strconv.ParseInt(text, 10, 64)
		return value
	}
	return 0
}

// ModelLister is an optional provider capability used by routing gateways.
type ModelLister interface {
	ListModels(context.Context) ([]Model, error)
}

// Stream returns chunks in order. Recv returns io.EOF after the final chunk.
type Stream interface {
	Recv() (*ChatChunk, error)
	Close() error
}

// ResponseHeaderer is an optional stream capability for HTTP response
// metadata such as provider cache status.
type ResponseHeaderer interface {
	ResponseHeaders() http.Header
}

// Provider is the minimal contract implemented by LLM backends.
type Provider interface {
	Chat(context.Context, ChatRequest) (*ChatResponse, error)
	ChatStream(context.Context, ChatRequest) (Stream, error)
	Close() error
}
