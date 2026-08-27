package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/memory"
)

// ContextCompactor gives isolated internal-agent loops the same summarizing
// memory behavior as the main chat while preserving their initial role/task
// messages verbatim. The full lifecycle archive remains untouched.
type ContextCompactor struct {
	memory         *memory.Manager
	anchorCount    int
	preservedTools []string
	usage          client.Usage
}

// PreserveTools keeps complete exchanges for semantically authoritative data
// such as a user's answer, a staged repository snapshot, or an acknowledged
// proposition. Other completed tool results remain eligible for summarization.
func (c *ContextCompactor) PreserveTools(names ...string) {
	c.preservedTools = append(c.preservedTools, names...)
}

// NewContextCompactor creates a loop-local context. The first anchorCount
// messages are retained verbatim through every compaction.
func NewContextCompactor(spec Spec, initial []client.Message, tools []client.Tool, anchorCount int) *ContextCompactor {
	manager := memory.New(spec.memoryPolicy(), initial)
	// Tool definitions consume prompt tokens on every role request. Seed them as
	// provider overhead so compaction still triggers when usage is unavailable.
	if toolTokens := countTools(tools); toolTokens > 0 {
		local := manager.LocalEstimate()
		manager.ObserveUsage(local+toolTokens, local)
	}
	return &ContextCompactor{
		memory: manager, anchorCount: min(max(anchorCount, 0), len(initial)),
	}
}

func (c *ContextCompactor) Messages() []client.Message {
	if c == nil || c.memory == nil {
		return nil
	}
	return c.memory.Messages()
}

// RequestMessages projects q's internal summary into ordinary conversation
// data. Anchored tasks include user/tool messages before the summary; sending
// a named system message there would break stricter provider chat templates.
func (c *ContextCompactor) RequestMessages() []client.Message {
	messages := c.Messages()
	for index := range messages {
		if messages[index].Name == memory.SummaryName {
			messages[index].Role = client.RoleUser
			messages[index].Name = ""
		}
	}
	return messages
}

func (c *ContextCompactor) Append(messages ...client.Message) {
	if c == nil || c.memory == nil {
		return
	}
	for _, message := range messages {
		c.memory.Append(message)
	}
}

// SetAnchor refreshes structured state that is maintained by the host, such
// as Thinker's acknowledged proposition ledger. It cannot replace history.
func (c *ContextCompactor) SetAnchor(index int, message client.Message) error {
	if c == nil || c.memory == nil || index < 0 || index >= c.anchorCount {
		return fmt.Errorf("subagent: context anchor index %d is out of range", index)
	}
	return c.memory.Replace(index, message)
}

func (c *ContextCompactor) CompactionUsage() client.Usage {
	return c.usage
}

// Observe calibrates provider/tool overhead from the latest role request.
// Call it before appending the response message so LocalEstimate describes the
// request that produced the usage record.
func (c *ContextCompactor) Observe(usage client.Usage) {
	if c == nil || c.memory == nil {
		return
	}
	c.memory.ObserveUsage(usage.PromptTokens, c.memory.LocalEstimate())
}

// CompactIfNeeded summarizes only completed loop history. It uses a fresh
// backend conversation for summarization and clears the role conversation
// after applying the new context so stateful providers cannot retain the
// discarded pre-compaction history.
func (c *ContextCompactor) CompactIfNeeded(ctx context.Context, spec *Spec, configured AgentClient) error {
	if c == nil || c.memory == nil || !c.memory.ShouldCompact() {
		return nil
	}
	if spec == nil || configured == nil {
		return errors.New("subagent: context compaction requires model spec and client")
	}
	plan, err := c.memory.PlanWithRetention(memory.Retention{
		ImmutablePrefix: c.anchorCount, PreserveToolNames: c.preservedTools, AllowTargetGrowth: true,
		SummarizeOversizedRecent: true,
	})
	if err != nil {
		return err
	}
	maxTokens := plan.OutputBudget
	if spec.MaxOutputTokens > 0 {
		maxTokens = min(maxTokens, int(spec.MaxOutputTokens))
	}
	compactor := *spec
	compactor.conversationID = ""
	response, err := compactor.Chat(ctx, configured, client.ChatRequest{
		Messages: plan.RequestMessages(), MaxCompletionTokens: &maxTokens,
	})
	if err != nil {
		return fmt.Errorf("subagent: compact context: %w", err)
	}
	if response == nil || len(response.Choices) == 0 {
		return errors.New("subagent: compact context returned no choices")
	}
	summary := strings.TrimSpace(response.Choices[0].Message.TextContent())
	if err := c.memory.Apply(plan, summary); err != nil {
		return fmt.Errorf("subagent: compact context: %w", err)
	}
	c.usage = addUsage(c.usage, response.Usage)
	spec.conversationID = ""
	return nil
}

func (s Spec) memoryPolicy() memory.Policy {
	contextConfig := s.ContextPolicy
	defaults := config.Default().Context
	if contextConfig.TriggerRatio == 0 {
		contextConfig.TriggerRatio = defaults.TriggerRatio
	}
	if contextConfig.TargetRatio == 0 {
		contextConfig.TargetRatio = defaults.TargetRatio
	}
	if contextConfig.RecentRatio == 0 {
		contextConfig.RecentRatio = defaults.RecentRatio
	}
	// Leave more headroom for the next tool result without invalidating an
	// explicitly configured target at or above 80%.
	triggerRatio := contextConfig.TriggerRatio
	if contextConfig.TargetRatio < .80 {
		triggerRatio = min(triggerRatio, .80)
	}
	return memory.Policy{
		ContextWindow: int(s.ContextLength), TriggerRatio: triggerRatio,
		TargetRatio: contextConfig.TargetRatio, RecentRatio: contextConfig.RecentRatio,
	}
}

func countTools(tools []client.Tool) int {
	if len(tools) == 0 {
		return 0
	}
	body, err := json.Marshal(tools)
	if err != nil {
		return len(tools) * 32
	}
	return (len(body)+2)/3 + len(tools)*4
}
