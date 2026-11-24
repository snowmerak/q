package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/snowmerak/q/internal/tokenestimate"
)

const (
	UsageRoleUnknown = "unknown"
	UsageRoleGateway = "gateway"

	// UsageEventIDHeader and UsageRoleHeader carry Q usage attribution to a
	// trusted Q Gateway. They are telemetry metadata, not authorization claims.
	UsageEventIDHeader = "X-Q-Usage-Event-ID"
	UsageRoleHeader    = "X-Q-Usage-Role"
)

type usageRoleContextKey struct{}
type usageEventIDContextKey struct{}

var usageIDFallback atomic.Uint64

// UsageRecord carries either token counts for a provider response or a model
// call outcome. CacheEstimated distinguishes unavailable cache data from zero.
type UsageRecord struct {
	EventID          string            `json:"event_id"`
	At               time.Time         `json:"at"`
	Model            string            `json:"model"`
	Role             string            `json:"role"`
	PromptTokens     int               `json:"prompt_tokens"`
	CompletionTokens int               `json:"completion_tokens"`
	TotalTokens      int               `json:"total_tokens"`
	CachedTokens     int               `json:"cached_tokens"`
	CacheWriteTokens int               `json:"cache_write_tokens"`
	Estimated        bool              `json:"estimated,omitempty"`
	CacheEstimated   bool              `json:"cache_estimated,omitempty"`
	Attribution      UsageAttribution  `json:"attribution,omitzero"`
	Performance      *UsagePerformance `json:"performance,omitempty"`
}

type UsageAttribution struct {
	ProjectID   string `json:"project_id,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
	Subagent    string `json:"subagent,omitempty"`
}

type UsagePerformance struct {
	Status             string   `json:"status"`
	DurationMS         int64    `json:"duration_ms"`
	TTFTMS             *int64   `json:"ttft_ms,omitempty"`
	OutputTokensPerSec *float64 `json:"output_tokens_per_second,omitempty"`
	Retried            bool     `json:"retried,omitempty"`
}

type usageAttributionKey struct{}

func usageAttribution(ctx context.Context) UsageAttribution {
	value, _ := ctx.Value(usageAttributionKey{}).(UsageAttribution)
	return value
}

// WithUsageProject associates local model usage with a Q project.
func WithUsageProject(ctx context.Context, id, name string) context.Context {
	value := usageAttribution(ctx)
	value.ProjectID, value.ProjectName = id, name
	return context.WithValue(ctx, usageAttributionKey{}, value)
}

// WithUsageSubagent associates local model usage with the executing profile.
func WithUsageSubagent(ctx context.Context, name string) context.Context {
	value := usageAttribution(ctx)
	value.Subagent = name
	return context.WithValue(ctx, usageAttributionKey{}, value)
}

// WithUsageRole labels model calls made through ctx with a bounded Q-owned
// execution role. Invalid or empty labels become "unknown" rather than
// admitting user-controlled strings into the telemetry dimension.
func WithUsageRole(ctx context.Context, role string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, usageRoleContextKey{}, NormalizeUsageRole(role))
}

func usageRole(ctx context.Context) string {
	if ctx == nil {
		return UsageRoleUnknown
	}
	role, _ := ctx.Value(usageRoleContextKey{}).(string)
	return NormalizeUsageRole(role)
}

func withUsageEventID(ctx context.Context, eventID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if !ValidUsageEventID(eventID) {
		return ctx
	}
	return context.WithValue(ctx, usageEventIDContextKey{}, eventID)
}

func usageEventID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	eventID, _ := ctx.Value(usageEventIDContextKey{}).(string)
	if !ValidUsageEventID(eventID) {
		return ""
	}
	return eventID
}

// UsageRecorder receives token records after provider responses. Recorder
// failures are diagnostic-only and never change the model call result.
type UsageRecorder interface {
	RecordUsage(UsageRecord) error
}

// ModelCallRecorder is an optional extension for completed call outcomes.
type ModelCallRecorder interface {
	RecordModelCall(UsageRecord) error
}

func (c *Client) recordChatUsage(ctx context.Context, request ChatRequest, responseModel string, usage Usage, completionEstimate int) {
	if c == nil || c.usageRecorder == nil {
		return
	}
	model := strings.TrimSpace(responseModel)
	if model == "" {
		model = strings.TrimSpace(request.Model)
	}
	record := normalizedUsageRecord(model, usage, estimateRequestTokens(request), completionEstimate)
	if eventID := usageEventID(ctx); eventID != "" {
		record.EventID = eventID
	}
	record.Role = usageRole(ctx)
	record.Attribution = usageAttribution(ctx)
	_ = c.usageRecorder.RecordUsage(record)
}

func (c *Client) recordEmbeddingUsage(ctx context.Context, request EmbeddingRequest, response *EmbeddingResponse) {
	if c == nil || c.usageRecorder == nil || response == nil {
		return
	}
	model := strings.TrimSpace(response.Model)
	if model == "" {
		model = strings.TrimSpace(request.Model)
	}
	reported := Usage{
		PromptTokens: response.Usage.PromptTokens,
		TotalTokens:  response.Usage.TotalTokens,
	}
	record := normalizedUsageRecord(model, reported, estimateEmbeddingTokens(request.Input), 0)
	if eventID := usageEventID(ctx); eventID != "" {
		record.EventID = eventID
	}
	record.Role = usageRole(ctx)
	record.Attribution = usageAttribution(ctx)
	if record.Role == UsageRoleUnknown {
		record.Role = "embedding"
	}
	_ = c.usageRecorder.RecordUsage(record)
}

func normalizedUsageRecord(model string, reported Usage, promptEstimate, completionEstimate int) UsageRecord {
	record := UsageRecord{
		EventID: NewUsageEventID(), At: time.Now().UTC(), Model: strings.TrimSpace(model), Role: UsageRoleUnknown,
	}
	record.PromptTokens = max(0, reported.PromptTokens)
	if record.PromptTokens == 0 {
		if reported.TotalTokens > 0 && reported.CompletionTokens > 0 && reported.TotalTokens >= reported.CompletionTokens {
			record.PromptTokens = reported.TotalTokens - reported.CompletionTokens
		} else if promptEstimate > 0 {
			record.PromptTokens = promptEstimate
			record.Estimated = true
		}
	}
	record.CompletionTokens = max(0, reported.CompletionTokens)
	if record.CompletionTokens == 0 {
		if reported.TotalTokens > 0 && record.PromptTokens > 0 && reported.TotalTokens >= record.PromptTokens {
			record.CompletionTokens = reported.TotalTokens - record.PromptTokens
		} else if completionEstimate > 0 {
			record.CompletionTokens = completionEstimate
			record.Estimated = true
		}
	}
	record.TotalTokens = max(0, reported.TotalTokens)
	if record.TotalTokens == 0 {
		record.TotalTokens = record.PromptTokens + record.CompletionTokens
	}
	if reported.PromptDetails == nil {
		record.CacheEstimated = true
		return record
	}
	record.CachedTokens = max(0, reported.PromptDetails.CachedTokens)
	record.CacheWriteTokens = max(0, reported.PromptDetails.CacheWriteTokens)
	return record
}

func estimateRequestTokens(request ChatRequest) int {
	return tokenestimate.Messages(request.Messages) +
		estimateJSONTokens(request.Tools, len(request.Tools), 4)
}

func estimateResponseTokens(response *ChatResponse) int {
	if response == nil {
		return 0
	}
	messages := make([]Message, 0, len(response.Choices))
	for _, choice := range response.Choices {
		messages = append(messages, choice.Message)
	}
	return estimateJSONTokens(messages, len(messages), 4)
}

func estimateEmbeddingTokens(input any) int {
	if input == nil {
		return 0
	}
	return estimateJSONTokens(input, 1, 0)
}

func estimateJSONTokens(value any, items, perItem int) int {
	if items == 0 {
		return 0
	}
	body, err := json.Marshal(value)
	if err != nil {
		return items * max(1, perItem*2)
	}
	return int(math.Ceil(float64(len(body))/3.0)) + items*perItem
}

type usageStream struct {
	inner           Stream
	client          *Client
	call            *modelCallLog
	request         ChatRequest
	model           string
	usage           Usage
	completionBytes int
	ctx             context.Context
	once            sync.Once
}

type usageHeaderStream struct {
	*usageStream
	headers ResponseHeaderer
}

func (s *usageHeaderStream) ResponseHeaders() http.Header {
	if s == nil || s.headers == nil {
		return nil
	}
	return s.headers.ResponseHeaders()
}

func (s *usageStream) Recv() (*ChatChunk, error) {
	chunk, err := s.inner.Recv()
	if s.call != nil && errors.Is(err, io.EOF) {
		// Usage persistence runs synchronously after the stream has ended.
		s.call.observeCompletion(time.Now())
	}
	if chunk != nil {
		s.observe(chunk)
	}
	if err != nil {
		if incomplete, ok := errors.AsType[*IncompleteGeneration](err); ok && incomplete.Response != nil {
			s.usage = incomplete.Response.Usage
		}
		s.finish()
	}
	return chunk, err
}

func (s *usageStream) Close() error {
	err := s.inner.Close()
	s.finish()
	return err
}

func (s *usageStream) observe(chunk *ChatChunk) {
	if chunk == nil {
		return
	}
	if model := strings.TrimSpace(chunk.Model); model != "" {
		s.model = model
	}
	if chunk.Usage != nil {
		s.usage = *chunk.Usage
	}
	for _, choice := range chunk.Choices {
		if choice.Delta == nil {
			continue
		}
		s.completionBytes += len(choice.Delta.TextContent())
		for _, call := range choice.Delta.ToolCalls {
			s.completionBytes += len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
}

func (s *usageStream) finish() {
	if s == nil || s.client == nil {
		return
	}
	s.once.Do(func() {
		model := s.model
		if model == "" {
			model = s.request.Model
		}
		completionEstimate := 0
		if s.completionBytes > 0 {
			completionEstimate = int(math.Ceil(float64(s.completionBytes)/3.0)) + 4
		}
		s.client.recordChatUsage(s.ctx, s.request, model, s.usage, completionEstimate)
	})
}

// NormalizeUsageRole returns a bounded low-cardinality role suitable for Q's
// usage dimension. Invalid values become UsageRoleUnknown.
func NormalizeUsageRole(role string) string {
	role = strings.TrimSpace(strings.ToLower(role))
	if role == "" || len(role) > 64 {
		return UsageRoleUnknown
	}
	for _, value := range role {
		if (value < 'a' || value > 'z') && (value < '0' || value > '9') && value != '_' && value != '-' {
			return UsageRoleUnknown
		}
	}
	return role
}

// NewUsageEventID returns a random lowercase hexadecimal idempotency ID.
func NewUsageEventID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("%016x%016x", time.Now().UTC().UnixNano(), usageIDFallback.Add(1))
}

// ValidUsageEventID reports whether value is a supported lowercase
// hexadecimal usage event ID.
func ValidUsageEventID(value string) bool {
	if value == "" || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && (len(decoded) == 16 || len(decoded) == 32)
}

var _ Stream = (*usageStream)(nil)
var _ Stream = (*usageHeaderStream)(nil)
var _ ResponseHeaderer = (*usageHeaderStream)(nil)
