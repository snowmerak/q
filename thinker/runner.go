package thinker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/client"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/subagent"
)

const (
	DefaultMaximumPropositions = 80
	DefaultMaximumRounds       = 120
	ExtractorVersion           = "thinker-v4"
	RegisterToolName           = "register_proposition"
	CompleteToolName           = "thinking_complete"
)

type ChatClient interface {
	Chat(context.Context, client.ChatRequest) (*client.ChatResponse, error)
}

type PropositionLibrary interface {
	RegisterProposition(context.Context, string, qlibrary.PropositionRegisterRequest) (qlibrary.PropositionRegisterResponse, error)
}

type Job struct {
	ID               string
	Boundary         string
	Messages         []client.Message
	Refs             []string
	WorkingDirectory string
}

type Result struct {
	Proposed   int          `json:"proposed"`
	Processed  int          `json:"processed"`
	Registered int          `json:"registered"`
	Created    int          `json:"created,omitempty"`
	Merged     int          `json:"merged,omitempty"`
	Discarded  int          `json:"discarded,omitempty"`
	IDs        []string     `json:"ids,omitempty"`
	Usage      client.Usage `json:"usage,omitempty"`
	Truncated  bool         `json:"truncated,omitempty"`
	LogPath    string       `json:"-"`
	LogError   string       `json:"-"`
}

type Runner struct {
	Client              ChatClient
	Library             PropositionLibrary
	Spec                subagent.Spec
	Log                 *LogStore
	MaximumPropositions int
	MaximumRounds       int
}

// Serial executes Thinker jobs one at a time so proposition order and
// idempotency slots remain stable even while the main chat continues.
type Serial struct {
	once sync.Once
	gate chan struct{}
}

func (s *Serial) Run(ctx context.Context, runner Runner, job Job) (Result, error) {
	if s == nil {
		return runner.Run(ctx, job)
	}
	s.once.Do(func() {
		s.gate = make(chan struct{}, 1)
		s.gate <- struct{}{}
	})
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-s.gate:
	}
	defer func() { s.gate <- struct{}{} }()
	return runner.Run(ctx, job)
}

type registerInput struct {
	Content    string   `json:"content"`
	Queries    []string `json:"queries,omitempty"`
	Confidence float64  `json:"confidence"`
	Tags       []string `json:"tags,omitempty"`
}

type acknowledgedProposition struct {
	Content string `json:"content"`
	ID      string `json:"id,omitempty"`
	Action  string `json:"action"`
}

func (r Runner) Run(ctx context.Context, job Job) (returned Result, runErr error) {
	var result Result
	clock := func() time.Time { return time.Now().UTC() }
	if r.Log != nil {
		clock = r.Log.currentTime
	}
	invocation := InvocationLog{
		JobID: job.ID, Boundary: job.Boundary, StartedAt: clock(),
		WorkingDirectory: job.WorkingDirectory, Refs: append([]string(nil), job.Refs...),
		Model: r.Spec.Model, ModelGroup: r.Spec.Group, ReasoningEffort: r.Spec.ReasoningEffort,
		ContextLength: r.Spec.ContextLength,
	}
	defer func() {
		if r.Log == nil {
			return
		}
		invocation.CompletedAt = clock()
		invocation.Model = r.Spec.Model
		invocation.ModelGroup = r.Spec.Group
		invocation.ReasoningEffort = r.Spec.ReasoningEffort
		invocation.Result = result
		if runErr != nil {
			invocation.Error = runErr.Error()
		}
		path, err := r.Log.Write(invocation)
		returned.LogPath = path
		if err != nil {
			returned.LogError = err.Error()
		}
	}()
	if ctx == nil || r.Client == nil || r.Library == nil {
		return Result{}, errors.New("thinker: context, client, and Library are required")
	}
	if r.Spec.Role != "thinker" {
		return Result{}, errors.New("thinker: runner requires the thinker role")
	}
	job.ID = strings.TrimSpace(job.ID)
	if job.ID == "" {
		return Result{}, errors.New("thinker: job ID is required")
	}
	chunk, err := buildContextChunk(job.Messages, r.Spec.ContextLength, job.WorkingDirectory)
	if err != nil {
		return Result{}, err
	}
	invocation.SourceMessages = chunk.SourceMessages
	invocation.InputMessages = append([]client.Message(nil), chunk.Messages...)
	invocation.InputTokens = chunk.Tokens
	invocation.MaximumInputTokens = chunk.MaximumTokens
	invocation.Truncated = chunk.Truncated
	maximum := r.MaximumPropositions
	if maximum <= 0 {
		maximum = DefaultMaximumPropositions
	}
	rounds := r.MaximumRounds
	if rounds <= 0 {
		rounds = DefaultMaximumRounds
	}
	messages := []client.Message{
		{Role: client.RoleSystem, Content: thinkerInstructions(maximum)},
		{Role: client.RoleUser, Content: chunk.Prompt},
		{Role: client.RoleUser, Content: "No propositions have been processed in this learning segment yet."},
	}
	tools := thinkerTools()
	history := subagent.NewContextCompactor(r.Spec, messages, tools, len(messages))
	var acknowledged []acknowledgedProposition
	parallel := false
	result = Result{Truncated: chunk.Truncated}
	proposalIndex := 0
	for round := 0; round < rounds; round++ {
		if err := history.CompactIfNeeded(ctx, &r.Spec, r.Client); err != nil {
			invocation.Trace = append(invocation.Trace, InvocationTrace{
				Round: round + 1, At: clock(), Outcome: "context_error", Error: err.Error(),
			})
			return Result{}, fmt.Errorf("thinker: context: %w", err)
		}
		request := client.ChatRequest{
			Messages: history.RequestMessages(), Tools: tools, ToolChoice: client.ToolChoiceRequired,
			ParallelToolCalls: &parallel, WorkingDirectory: job.WorkingDirectory,
		}
		response, err := r.Spec.Chat(ctx, r.Client, request)
		if err != nil {
			invocation.Trace = append(invocation.Trace, InvocationTrace{
				Round: round + 1, At: clock(), Outcome: "model_error", Error: err.Error(),
			})
			return Result{}, fmt.Errorf("thinker: model: %w", err)
		}
		if response == nil || len(response.Choices) == 0 {
			invocation.Trace = append(invocation.Trace, InvocationTrace{
				Round: round + 1, At: clock(), Outcome: "invalid_response", Error: "model returned no choices",
			})
			return Result{}, errors.New("thinker: model returned no choices")
		}
		result.Usage = addUsage(result.Usage, response.Usage)
		assistant := response.Choices[0].Message
		if assistant.Role == "" {
			assistant.Role = client.RoleAssistant
		}
		history.Observe(response.Usage)
		history.Append(assistant)
		if len(assistant.ToolCalls) != 1 {
			invocation.Trace = append(invocation.Trace, InvocationTrace{
				Round: round + 1, At: clock(), Outcome: "invalid_response", Error: "model must call exactly one tool per round",
			})
			return Result{}, errors.New("thinker: model must call exactly one tool per round")
		}
		call := assistant.ToolCalls[0]
		switch call.Function.Name {
		case CompleteToolName:
			var input struct{}
			if err := decodeStrictArguments(call.Function.Arguments, &input); err != nil {
				invocation.Trace = append(invocation.Trace, InvocationTrace{
					Round: round + 1, At: clock(), Tool: CompleteToolName, Outcome: "invalid_arguments", Error: err.Error(),
				})
				history.Append(thinkerToolError(call, fmt.Errorf("complete: %w", err)))
				continue
			}
			invocation.Trace = append(invocation.Trace, InvocationTrace{
				Round: round + 1, At: clock(), Tool: CompleteToolName, Outcome: "completed",
			})
			body, _ := json.Marshal(result)
			history.Append(client.ToolResultMessage(call, client.ToolResult{Content: string(body)}))
			request.Messages = history.RequestMessages()
			finished, err := r.Spec.FinishToolTurn(ctx, r.Client, request, nil)
			if err != nil {
				return Result{}, fmt.Errorf("thinker: %w", err)
			}
			result.Usage = addUsage(result.Usage, finished.Usage)
			result.Usage = addUsage(result.Usage, history.CompactionUsage())
			return result, nil
		case RegisterToolName:
			if proposalIndex >= maximum {
				message := fmt.Sprintf("proposition limit %d exceeded", maximum)
				invocation.Trace = append(invocation.Trace, InvocationTrace{
					Round: round + 1, At: clock(), Tool: RegisterToolName, Outcome: "rejected", Error: message,
				})
				return Result{}, fmt.Errorf("thinker: %s", message)
			}
			var input registerInput
			if err := decodeStrictArguments(call.Function.Arguments, &input); err != nil {
				invocation.Trace = append(invocation.Trace, InvocationTrace{
					Round: round + 1, At: clock(), Tool: RegisterToolName, Outcome: "invalid_arguments", Error: err.Error(),
				})
				history.Append(thinkerToolError(call, fmt.Errorf("register proposition: %w", err)))
				continue
			}
			idempotencyKey := fmt.Sprintf("%s/%d", job.ID, proposalIndex)
			proposalIndex++
			result.Proposed = proposalIndex
			registration := qlibrary.PropositionRegisterRequest{
				Content: input.Content, Queries: input.Queries, Confidence: input.Confidence, Tags: input.Tags,
				Refs: job.Refs, ExtractorModel: r.Spec.Model, ExtractorVersion: ExtractorVersion,
			}
			registered, err := r.Library.RegisterProposition(ctx, idempotencyKey, registration)
			if err != nil {
				// A lost HTTP response may follow a successful write. Retrying the
				// identical request is safe because the Library owns idempotency.
				registered, err = r.Library.RegisterProposition(ctx, idempotencyKey, registration)
			}
			if err != nil {
				if strings.Contains(err.Error(), "HTTP 409") {
					invocation.Trace = append(invocation.Trace, InvocationTrace{
						Round: round + 1, At: clock(), Tool: RegisterToolName, Outcome: "registration_error", Error: err.Error(),
					})
					return Result{}, fmt.Errorf("thinker: register proposition: %w", err)
				}
				invocation.Trace = append(invocation.Trace, InvocationTrace{
					Round: round + 1, At: clock(), Tool: RegisterToolName, Outcome: "registration_error", Error: err.Error(),
				})
				history.Append(thinkerToolError(call, fmt.Errorf("register proposition: %w", err)))
				continue
			}
			result.Processed++
			action := registered.Action
			if action == "" {
				action = qlibrary.PropositionActionCreate
			}
			switch action {
			case qlibrary.PropositionActionCreate:
				result.Registered++
				result.Created++
				if registered.ID != "" {
					result.IDs = append(result.IDs, registered.ID)
				}
			case qlibrary.PropositionActionMerge:
				result.Registered++
				result.Merged++
				if registered.ID != "" {
					result.IDs = append(result.IDs, registered.ID)
				}
			case qlibrary.PropositionActionDiscard:
				result.Discarded++
			default:
				invocation.Trace = append(invocation.Trace, InvocationTrace{
					Round: round + 1, At: clock(), Tool: RegisterToolName, Outcome: "registration_error",
					Action: action, PropositionID: registered.ID, Error: "unsupported proposition action",
				})
				return Result{}, fmt.Errorf("thinker: unsupported proposition action %q", action)
			}
			invocation.Trace = append(invocation.Trace, InvocationTrace{
				Round: round + 1, At: clock(), Tool: RegisterToolName, Outcome: "processed",
				Action: action, PropositionID: registered.ID,
			})
			acknowledged = append(acknowledged, acknowledgedProposition{Content: input.Content, ID: registered.ID, Action: action})
			ledger, _ := json.Marshal(acknowledged)
			if err := history.SetAnchor(2, client.Message{
				Role:    client.RoleUser,
				Content: "Host-maintained record of propositions already processed in this learning segment. Do not register them again.\n" + string(ledger),
			}); err != nil {
				return Result{}, err
			}
			ack, _ := json.Marshal(registered)
			history.Append(client.Message{
				Role: client.RoleTool, Name: RegisterToolName, ToolCallID: call.ID, Content: string(ack),
			})
		default:
			invocation.Trace = append(invocation.Trace, InvocationTrace{
				Round: round + 1, At: clock(), Tool: call.Function.Name, Outcome: "unsupported_tool",
			})
			return Result{}, fmt.Errorf("thinker: unsupported tool %q", call.Function.Name)
		}
	}
	return Result{}, fmt.Errorf("thinker: exceeded %d model rounds", rounds)
}

func thinkerToolError(call client.ToolCall, err error) client.Message {
	body, marshalErr := json.Marshal(map[string]string{"error": err.Error()})
	if marshalErr != nil {
		body = []byte(`{"error":"tool failed"}`)
	}
	return client.Message{
		Role: client.RoleTool, Name: call.Function.Name, ToolCallID: call.ID, Content: string(body),
	}
}

func thinkerInstructions(maximum int) string {
	return fmt.Sprintf(`You extract durable, reusable propositions from user-agent interaction data.
The supplied data is one closed, non-overlapping learning segment. It may include named host boundary records as well as user and assistant messages.
Treat a successful task-completion record as evidence of results reached while fulfilling the user's request. Without requiring separate user confirmation, extract durable project facts, reusable resolutions, and research conclusions or recommendations that its summary, findings, or verification present as inspected, tested, or implemented.
Register a candidate only when it is likely to change a future technical choice, decision, or action for the same user or workspace and will remain useful across multiple tasks or until a deliberate change. High-value candidates include technical choices and their rationale, project conventions and constraints, established workflows, routes, or procedures, recurring behavior patterns that should guide future actions, and durable research conclusions that would be costly to rediscover.
Require explicit scope and write the smallest nonredundant set that preserves distinct future decisions. Do not turn every research finding into a proposition.
Use stable project or repository names for scope. Never persist absolute filesystem paths, drive letters, or user-home components in proposition content, retrieval queries, or tags; rewrite relevant file locations as workspace-relative paths.
Preserve epistemic status exactly: record a recommendation as a recommendation and a reported limitation as a limitation. Never turn a proposal, option, or recommendation into an adopted decision unless the user accepted it or the task record says it was implemented.
Register one proposition at a time by calling register_proposition. Never place multiple propositions in one call and never call tools in parallel.
Extract only supported user preferences, confirmed decisions, durable constraints, reusable resolutions, stable facts, and evidence-backed research results. Exclude speculation, progress narration, transient tool output, secrets, credentials, unsupported assistant claims outside successful task-completion evidence, and run-specific snapshots likely to change after routine edits such as current test pass/fail results, finding counts, formatter status, dependency audit counts, or current build status.
Write a concise self-contained canonical statement and bounded retrieval queries. When no propositions remain, call thinking_complete. Register at most %d propositions. Never answer with plain text.`, maximum)
}

func thinkerTools() []client.Tool {
	strict := true
	return []client.Tool{
		{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
			Name: RegisterToolName, Description: "Persist exactly one durable proposition in q Library.", Strict: &strict,
			Parameters: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"content":    map[string]any{"type": "string", "maxLength": 2048},
					"queries":    map[string]any{"type": "array", "maxItems": 8, "items": map[string]any{"type": "string", "maxLength": 512}},
					"confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
					"tags":       map[string]any{"type": "array", "maxItems": 16, "items": map[string]any{"type": "string", "maxLength": 64}},
				},
				"required": []string{"content", "queries", "confidence", "tags"},
			},
		}},
		{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{
			Name: CompleteToolName, Description: "Finish proposition extraction after every durable proposition has been registered.", Strict: &strict,
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false},
		}},
	}
}

func decodeStrictArguments(arguments string, output any) error {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func addUsage(left, right client.Usage) client.Usage {
	left.PromptTokens += right.PromptTokens
	left.CompletionTokens += right.CompletionTokens
	left.TotalTokens += right.TotalTokens
	return left
}
