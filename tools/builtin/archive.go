package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/snowmerak/q/sessionstore"
)

const (
	defaultArchiveSearchLimit  = 8
	maximumArchiveSearchLimit  = 50
	archiveSearchExcerptRunes  = 1200
	archiveSearchSummaryRunes  = 600
	defaultRecordContentRunes  = 12000
	maximumRecordContentRunes  = 50000
	maximumRecordSummaryRunes  = 4000
	maximumRecordPayloadBytes  = 64 << 10
	defaultRecencyHalfLifeHour = 24 * 30
	maximumRecencyHalfLifeHour = 24 * 365 * 100
	maximumRecencyWeight       = 100
	maximumArchiveSearchOffset = 5000
)

// Archive is the read side of the workspace record store.
type Archive interface {
	Search(context.Context, sessionstore.SearchOptions) (sessionstore.SearchResult, error)
	Get(string) (sessionstore.Record, error)
}

type SearchArchiveInput struct {
	Query                string   `json:"query,omitempty" jsonschema:"Text to match against record summaries and content."`
	Kinds                []string `json:"kinds,omitempty" jsonschema:"Record kinds to include, such as message, task, event, result, question, artifact, or summary."`
	Roles                []string `json:"roles,omitempty" jsonschema:"Roles to include, such as user, assistant, planner, coder, advisor, or tool."`
	Statuses             []string `json:"statuses,omitempty" jsonschema:"Statuses to include, such as submitted, queued, running, succeeded, failed, or cancelled."`
	RunIDs               []string `json:"run_ids,omitempty" jsonschema:"Archive run IDs to include."`
	TaskIDs              []string `json:"task_ids,omitempty" jsonschema:"Agent or tool task IDs to include."`
	ParentIDs            []string `json:"parent_ids,omitempty" jsonschema:"Parent record IDs to include."`
	Models               []string `json:"models,omitempty" jsonschema:"Model IDs to include."`
	Efforts              []string `json:"efforts,omitempty" jsonschema:"Reasoning efforts to include."`
	Tags                 []string `json:"tags,omitempty" jsonschema:"Tags to include."`
	CreatedAfter         string   `json:"created_after,omitempty" jsonschema:"Inclusive RFC3339 lower bound for created_at."`
	CreatedBefore        string   `json:"created_before,omitempty" jsonschema:"Inclusive RFC3339 upper bound for created_at."`
	Sort                 string   `json:"sort,omitempty" jsonschema:"Sort order: relevance, newest, or oldest. Defaults to relevance with a query and newest without one."`
	RecencyWeight        float64  `json:"recency_weight,omitempty" jsonschema:"Optional relevance boost for newer records, from 0 to 100. A positive value enables recency reranking."`
	RecencyHalfLifeHours float64  `json:"recency_half_life_hours,omitempty" jsonschema:"Recency decay half-life in hours. Defaults to 720 when recency_weight is positive."`
	Limit                int      `json:"limit,omitempty" jsonschema:"Maximum results, from 1 to 50. Defaults to 8."`
	Offset               int      `json:"offset,omitempty" jsonschema:"Non-negative result offset for pagination."`
}

type ArchiveSearchHit struct {
	ID               string    `json:"id"`
	Kind             string    `json:"kind"`
	RunID            string    `json:"run_id,omitempty"`
	TaskID           string    `json:"task_id,omitempty"`
	ParentID         string    `json:"parent_id,omitempty"`
	Role             string    `json:"role,omitempty"`
	Model            string    `json:"model,omitempty"`
	Effort           string    `json:"effort,omitempty"`
	Status           string    `json:"status,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Summary          string    `json:"summary,omitempty"`
	SummaryTruncated bool      `json:"summary_truncated,omitempty"`
	Excerpt          string    `json:"excerpt,omitempty"`
	Truncated        bool      `json:"truncated,omitempty"`
	Refs             []string  `json:"refs,omitempty"`
	Tags             []string  `json:"tags,omitempty"`
	Score            float64   `json:"score,omitempty"`
}

type SearchArchiveOutput struct {
	Total uint64             `json:"total"`
	Hits  []ArchiveSearchHit `json:"hits"`
}

type GetArchiveRecordInput struct {
	ID             string `json:"id" jsonschema:"Exact record ID returned by search_archive."`
	ContentOffset  int    `json:"content_offset,omitempty" jsonschema:"Unicode character offset into content. Defaults to zero."`
	ContentLimit   int    `json:"content_limit,omitempty" jsonschema:"Maximum content characters to return, from 1 to 50000. Defaults to 12000."`
	IncludePayload bool   `json:"include_payload,omitempty" jsonschema:"Include structured payload when it is at most 65536 encoded bytes. Defaults to false."`
}

type ArchiveEmbeddingMetadata struct {
	Model      string    `json:"model"`
	Dimensions int       `json:"dimensions"`
	CreatedAt  time.Time `json:"created_at"`
}

type GetArchiveRecordOutput struct {
	ID                string                    `json:"id"`
	Kind              string                    `json:"kind"`
	Version           int                       `json:"version"`
	RunID             string                    `json:"run_id,omitempty"`
	TaskID            string                    `json:"task_id,omitempty"`
	ParentID          string                    `json:"parent_id,omitempty"`
	Role              string                    `json:"role,omitempty"`
	Model             string                    `json:"model,omitempty"`
	Effort            string                    `json:"effort,omitempty"`
	Status            string                    `json:"status,omitempty"`
	CreatedAt         time.Time                 `json:"created_at"`
	UpdatedAt         time.Time                 `json:"updated_at"`
	Summary           string                    `json:"summary,omitempty"`
	SummaryTruncated  bool                      `json:"summary_truncated,omitempty"`
	Content           string                    `json:"content,omitempty"`
	ContentOffset     int                       `json:"content_offset"`
	NextContentOffset int                       `json:"next_content_offset"`
	MoreContent       bool                      `json:"more_content,omitempty"`
	Refs              []string                  `json:"refs,omitempty"`
	Tags              []string                  `json:"tags,omitempty"`
	Embedding         *ArchiveEmbeddingMetadata `json:"embedding,omitempty"`
	Payload           any                       `json:"payload,omitempty"`
	PayloadOmitted    bool                      `json:"payload_omitted,omitempty"`
	PayloadBytes      int                       `json:"payload_bytes,omitempty"`
}

func SearchArchive(ctx context.Context, archive Archive, input SearchArchiveInput) (SearchArchiveOutput, error) {
	if archive == nil {
		return SearchArchiveOutput{}, errors.New("[E_ARCHIVE] workspace archive is unavailable")
	}
	if utf8.RuneCountInString(input.Query) > 16000 {
		return SearchArchiveOutput{}, errors.New("[E_ARCHIVE] query exceeds 16000 characters")
	}
	limit := input.Limit
	if limit == 0 {
		limit = defaultArchiveSearchLimit
	}
	if limit < 1 || limit > maximumArchiveSearchLimit {
		return SearchArchiveOutput{}, fmt.Errorf("[E_ARCHIVE] limit must be between 1 and %d", maximumArchiveSearchLimit)
	}
	if input.Offset < 0 || input.Offset > maximumArchiveSearchOffset {
		return SearchArchiveOutput{}, fmt.Errorf("[E_ARCHIVE] offset must be between 0 and %d", maximumArchiveSearchOffset)
	}
	after, err := parseArchiveTime("created_after", input.CreatedAfter)
	if err != nil {
		return SearchArchiveOutput{}, err
	}
	before, err := parseArchiveTime("created_before", input.CreatedBefore)
	if err != nil {
		return SearchArchiveOutput{}, err
	}
	sortOrder := sessionstore.SortOrder(strings.ToLower(strings.TrimSpace(input.Sort)))
	if sortOrder == "" {
		if strings.TrimSpace(input.Query) == "" && input.RecencyWeight == 0 {
			sortOrder = sessionstore.SortNewest
		} else {
			sortOrder = sessionstore.SortRelevance
		}
	}
	kinds := input.Kinds
	if len(kinds) == 0 {
		kinds = []string{
			sessionstore.KindMessage, sessionstore.KindTask, sessionstore.KindEvent,
			sessionstore.KindResult, sessionstore.KindQuestion, sessionstore.KindArtifact,
			sessionstore.KindSummary,
		}
	}
	options := sessionstore.SearchOptions{
		Text: input.Query,
		Filters: sessionstore.Filters{
			RunIDs: input.RunIDs, TaskIDs: input.TaskIDs, ParentIDs: input.ParentIDs,
			Kinds: kinds, Roles: input.Roles, Models: input.Models,
			Efforts: input.Efforts, Statuses: input.Statuses, Tags: input.Tags,
		},
		CreatedAfter: after, CreatedBefore: before, Sort: sortOrder, Limit: limit, Offset: input.Offset,
	}
	if math.IsNaN(input.RecencyWeight) || math.IsInf(input.RecencyWeight, 0) ||
		input.RecencyWeight < 0 || input.RecencyWeight > maximumRecencyWeight {
		return SearchArchiveOutput{}, fmt.Errorf("[E_ARCHIVE] recency_weight must be between 0 and %d", maximumRecencyWeight)
	}
	if math.IsNaN(input.RecencyHalfLifeHours) || math.IsInf(input.RecencyHalfLifeHours, 0) ||
		input.RecencyHalfLifeHours < 0 || input.RecencyHalfLifeHours > maximumRecencyHalfLifeHour {
		return SearchArchiveOutput{}, fmt.Errorf("[E_ARCHIVE] recency_half_life_hours must be between 0 and %d", maximumRecencyHalfLifeHour)
	}
	if input.RecencyWeight == 0 && input.RecencyHalfLifeHours > 0 {
		return SearchArchiveOutput{}, errors.New("[E_ARCHIVE] recency_half_life_hours requires a positive recency_weight")
	}
	if input.RecencyWeight > 0 {
		halfLifeHours := input.RecencyHalfLifeHours
		if halfLifeHours == 0 {
			halfLifeHours = defaultRecencyHalfLifeHour
		}
		if halfLifeHours <= 0 {
			return SearchArchiveOutput{}, errors.New("[E_ARCHIVE] recency_half_life_hours must be positive")
		}
		options.Recency = &sessionstore.Recency{
			Weight: input.RecencyWeight, HalfLife: time.Duration(halfLifeHours * float64(time.Hour)),
		}
	}
	result, err := archive.Search(ctx, options)
	if err != nil {
		return SearchArchiveOutput{}, fmt.Errorf("[E_ARCHIVE] search: %w", err)
	}
	output := SearchArchiveOutput{Total: result.Total, Hits: make([]ArchiveSearchHit, 0, len(result.Hits))}
	for _, hit := range result.Hits {
		excerpt, truncated := truncateArchiveText(strings.TrimSpace(hit.Record.Content), archiveSearchExcerptRunes)
		summary, summaryTruncated := truncateArchiveText(strings.TrimSpace(hit.Record.Summary), archiveSearchSummaryRunes)
		output.Hits = append(output.Hits, ArchiveSearchHit{
			ID: hit.Record.ID, Kind: hit.Record.Kind, RunID: hit.Record.RunID,
			TaskID: hit.Record.TaskID, ParentID: hit.Record.ParentID, Role: hit.Record.Role,
			Model: hit.Record.Model, Effort: hit.Record.Effort, Status: hit.Record.Status,
			CreatedAt: hit.Record.CreatedAt, UpdatedAt: hit.Record.UpdatedAt,
			Summary: summary, SummaryTruncated: summaryTruncated, Excerpt: excerpt, Truncated: truncated,
			Refs: append([]string(nil), hit.Record.Refs...), Tags: append([]string(nil), hit.Record.Tags...),
			Score: hit.Score,
		})
	}
	return output, nil
}

func GetArchiveRecord(archive Archive, input GetArchiveRecordInput) (GetArchiveRecordOutput, error) {
	if archive == nil {
		return GetArchiveRecordOutput{}, errors.New("[E_ARCHIVE] workspace archive is unavailable")
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		return GetArchiveRecordOutput{}, errors.New("[E_ARCHIVE] record ID is required")
	}
	if input.ContentOffset < 0 {
		return GetArchiveRecordOutput{}, errors.New("[E_ARCHIVE] content_offset must not be negative")
	}
	limit := input.ContentLimit
	if limit == 0 {
		limit = defaultRecordContentRunes
	}
	if limit < 1 || limit > maximumRecordContentRunes {
		return GetArchiveRecordOutput{}, fmt.Errorf("[E_ARCHIVE] content_limit must be between 1 and %d", maximumRecordContentRunes)
	}
	record, err := archive.Get(id)
	if err != nil {
		return GetArchiveRecordOutput{}, fmt.Errorf("[E_ARCHIVE] get record: %w", err)
	}
	contentRunes := []rune(record.Content)
	if input.ContentOffset > len(contentRunes) {
		return GetArchiveRecordOutput{}, fmt.Errorf("[E_ARCHIVE] content_offset %d exceeds content length %d", input.ContentOffset, len(contentRunes))
	}
	end := min(len(contentRunes), input.ContentOffset+limit)
	summary, summaryTruncated := truncateArchiveText(record.Summary, maximumRecordSummaryRunes)
	output := GetArchiveRecordOutput{
		ID: record.ID, Kind: record.Kind, Version: record.Version,
		RunID: record.RunID, TaskID: record.TaskID, ParentID: record.ParentID,
		Role: record.Role, Model: record.Model, Effort: record.Effort, Status: record.Status,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt, Summary: summary,
		SummaryTruncated: summaryTruncated,
		Content:          string(contentRunes[input.ContentOffset:end]), ContentOffset: input.ContentOffset,
		NextContentOffset: end, MoreContent: end < len(contentRunes),
		Refs: append([]string(nil), record.Refs...), Tags: append([]string(nil), record.Tags...),
	}
	if record.Embedding != nil {
		output.Embedding = &ArchiveEmbeddingMetadata{
			Model: record.Embedding.Model, Dimensions: record.Embedding.Dimensions,
			CreatedAt: record.Embedding.CreatedAt,
		}
	}
	if input.IncludePayload && len(record.Payload) > 0 {
		output.PayloadBytes = len(record.Payload)
		if len(record.Payload) > maximumRecordPayloadBytes {
			output.PayloadOmitted = true
		} else {
			if err := json.Unmarshal(record.Payload, &output.Payload); err != nil {
				return GetArchiveRecordOutput{}, fmt.Errorf("[E_ARCHIVE] decode payload: %w", err)
			}
		}
	}
	return output, nil
}

func parseArchiveTime(field, value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("[E_ARCHIVE] %s must be RFC3339: %w", field, err)
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func truncateArchiveText(value string, limit int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	return string(runes[:limit]), true
}
