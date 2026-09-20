package agentloop

import (
	"context"
	"errors"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/memory"
)

var (
	// ErrInteractionUnavailable lets a headless host tell the loop that it
	// cannot answer a model-requested question. The loop returns that condition
	// to the model as a tool error so it can continue or finish as blocked.
	ErrInteractionUnavailable = errors.New("agentloop: interactive input is unavailable")

	// ErrRoundLimit is returned when Request.MaxRounds is reached before the
	// model produces a terminal response.
	ErrRoundLimit = errors.New("agentloop: model round limit reached")
)

// ModelClient is the only required model capability. Run never closes the
// client; the caller that created it owns its lifetime.
type ModelClient interface {
	Chat(context.Context, client.ChatRequest) (*client.ChatResponse, error)
}

// StreamingModelClient is detected as an optional ModelClient capability when
// Request.Stream is true. Clients that do not implement it use Chat instead.
type StreamingModelClient interface {
	ModelClient
	ChatStream(context.Context, client.ChatRequest) (client.Stream, error)
}

// ToolRuntime exposes model-visible schemas and dispatches one selected tool.
// Run calls tools sequentially. A runtime shared by concurrent Run calls must
// provide its own concurrency safety.
type ToolRuntime interface {
	Tools() []client.Tool
	Call(context.Context, client.ToolCall) (client.ToolResult, error)
}

// SkillSearchHit is trusted host metadata used to suggest applicable Agent
// Skills without fabricating a model-visible tool exchange.
type SkillSearchHit struct {
	ID          string
	Title       string
	Description string
	Tags        []string
	Scope       string
}

// SkillSearchResult is the bounded result returned by SkillHintSearcher.
type SkillSearchResult struct {
	Hits []SkillSearchHit
}

// SkillHintSearcher is an optional host capability. When it is absent or its
// search fails, the loop continues without automatic skill hints.
type SkillHintSearcher interface {
	SearchSkillHints(context.Context, string, int) (SkillSearchResult, error)
}

// Task is the active task lifecycle carried across turns.
type Task struct {
	Objective          string
	CompletionCriteria []string
	StartedAt          time.Time
}

// Request describes one agent turn. Messages are copied into loop-local
// context; Run does not mutate the caller's slice.
type Request struct {
	Client               ModelClient
	Tools                ToolRuntime
	SkillHints           SkillHintSearcher
	Messages             []client.Message
	Model                string
	ReasoningEffort      string
	ConversationID       string
	WorkingDirectory     string
	ActiveTask           *Task
	ContextPolicy        memory.Policy
	Stream               bool
	CoalesceInstructions bool

	// UsageRole labels provider usage. Empty defaults to "main".
	UsageRole string

	// MaxRounds bounds model calls for this turn. Zero preserves q's ordinary
	// behavior of running until completion or context cancellation.
	MaxRounds int
}

// AskFunc synchronously resolves a model-requested question. Returning
// ErrInteractionUnavailable produces a tool error and lets the model continue;
// any other error stops the run.
type AskFunc func(context.Context, Question) (Answer, error)

// EventSink receives ephemeral events synchronously and in execution order.
// Returning an error stops the run. Callers that need durable history must
// persist events before returning nil.
type EventSink func(Event) error

// Hooks are caller-owned delivery and interaction boundaries.
type Hooks struct {
	Event EventSink
	Ask   AskFunc
}

// Result is the terminal state of one Run. Context is the compacted
// loop-local model context, including the final assistant response; it is not
// necessarily the caller's full durable transcript.
type Result struct {
	Response        *client.ChatResponse
	Context         []client.Message
	ConversationID  string
	Outcome         string
	RequestEstimate int
	ToolCalls       int
}

// EventKind identifies which Event payload is populated.
type EventKind string

const (
	EventStatus         EventKind = "status"
	EventMessage        EventKind = "message"
	EventToolCall       EventKind = "tool_call"
	EventStreamDelta    EventKind = "stream_delta"
	EventContextReplace EventKind = "context_replace"
	EventCompaction     EventKind = "compaction"
	EventTaskStarted    EventKind = "task_started"
	EventTaskCompleted  EventKind = "task_completed"
)

// StreamKind distinguishes provider reasoning from user-visible response text.
type StreamKind string

const (
	StreamThinking StreamKind = "thinking"
	StreamResponse StreamKind = "response"
)

// StreamDelta is one ordered provider text fragment.
type StreamDelta struct {
	Kind    StreamKind
	Content string
	Start   bool
}

// ContextReplacement updates one existing message before the next model call.
type ContextReplacement struct {
	Index   int
	Message client.Message
}

// Compaction transfers a loop-local checkpoint to a caller that maintains a
// separate durable context projection.
type Compaction struct {
	Plan    memory.Plan
	Summary string
}

// Event is a tagged, immutable-by-contract notification. The field matching
// Kind is populated; receivers must not mutate slices or maps in its payload.
type Event struct {
	Kind EventKind

	Status      string
	Message     client.Message
	ToolCall    client.ToolCall
	ToolIsError bool
	Stream      StreamDelta
	Replacement ContextReplacement
	Compaction  Compaction
	Task        Task
	Completion  Completion
}
