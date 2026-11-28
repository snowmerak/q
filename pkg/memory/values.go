package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/snowmerak/q/v2/pkg/domain"
	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
	"github.com/snowmerak/q/v2/pkg/storage"
)

const (
	defaultValuesTargetRatio = 0.10
	defaultValuesMaxRatio    = 0.20
	maxValueEdits            = 256
	maxValueTextRunes        = 4000
	maxValueJobAttempts      = 5
)

// ValueEdit expresses one add/update/remove operation returned by consolidation.
type ValueEdit struct {
	ID   *string `json:"id"`
	Text *string `json:"text"`
}

// RenderValues formats character values consistently for token accounting and prompts.
func RenderValues(values []domain.CharacterValue) string {
	if len(values) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Current Values\n")
	for _, v := range values {
		b.WriteString(v.ID)
		b.WriteString(": ")
		b.WriteString(v.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// AssertValuesBudget rejects value sets exceeding ownBudget * maxRatio.
func AssertValuesBudget(values []domain.CharacterValue, ownBudget int, maxRatio float64) error {
	if len(values) == 0 || ownBudget <= 0 || maxRatio <= 0 {
		return nil
	}
	tokens := EstimateTokens(RenderValues(values))
	limit := int(float64(ownBudget) * maxRatio)
	if tokens > limit {
		return fmt.Errorf("%w: %d > %d tokens", ErrValuesTooLarge, tokens, limit)
	}
	return nil
}

// ApplyValueEdits validates and applies value edits onto prev deterministically.
func ApplyValueEdits(prev []domain.CharacterValue, jobID string, edits []ValueEdit) ([]domain.CharacterValue, error) {
	if len(edits) > maxValueEdits {
		return nil, fmt.Errorf("memory: value edits exceed max %d", maxValueEdits)
	}
	byID := make(map[string]string, len(prev))
	order := make([]string, 0, len(prev))
	for _, v := range prev {
		byID[v.ID] = v.Text
		order = append(order, v.ID)
	}
	changed := make(map[string]struct{})

	for idx, edit := range edits {
		if edit.ID == nil && edit.Text == nil {
			return nil, errors.New("memory: value edit cannot have both null id and null text")
		}
		if edit.ID != nil {
			id := *edit.ID
			if _, exists := byID[id]; !exists {
				return nil, fmt.Errorf("memory: value edit references unknown id %q", id)
			}
			if _, dup := changed[id]; dup {
				return nil, fmt.Errorf("memory: duplicate edit for value id %q", id)
			}
			changed[id] = struct{}{}
			if edit.Text == nil {
				delete(byID, id)
				continue
			}
			txt := strings.TrimSpace(*edit.Text)
			if txt == "" || utf8.RuneCountInString(txt) > maxValueTextRunes {
				return nil, fmt.Errorf("memory: invalid replacement text for value %q", id)
			}
			byID[id] = txt
			continue
		}

		txt := strings.TrimSpace(*edit.Text)
		if txt == "" || utf8.RuneCountInString(txt) > maxValueTextRunes {
			return nil, errors.New("memory: invalid new value text")
		}
		newID := "v-" + domain.HashInput(jobID + ":" + strconv.Itoa(idx))[:16]
		for slices.Contains(order, newID) {
			newID += "x"
		}
		order = append(order, newID)
		byID[newID] = txt
	}

	out := make([]domain.CharacterValue, 0, len(order))
	for _, id := range order {
		if txt, ok := byID[id]; ok {
			out = append(out, domain.CharacterValue{ID: id, Text: txt})
		}
	}
	return out, nil
}

// ExtractCandidatesParams configures pre-compaction candidate value extraction.
type ExtractCandidatesParams struct {
	Identity        string
	MemoryModel     string
	ReasoningEffort string
	ExistingValues  []domain.CharacterValue
	CompressedMemo  string
	SegmentText     string
	MaxTokens       int
	NowMS           int64
}

// ExtractValueCandidates extracts provisional character value statements from
// a segment about to be compacted.
func ExtractValueCandidates(ctx context.Context, chat ChatCompleter, params ExtractCandidatesParams) ([]string, error) {
	if chat == nil || strings.TrimSpace(params.MemoryModel) == "" || strings.TrimSpace(params.SegmentText) == "" {
		return nil, nil
	}
	nowMS := params.NowMS
	if nowMS == 0 {
		nowMS = domain.NowMS()
	}
	maxTokens := params.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 512
	}
	payload, _ := json.Marshal(map[string]any{
		"occurredAt": time.UnixMilli(nowMS).UTC().Format(time.RFC3339),
		"values":     params.ExistingValues,
		"memo":       params.CompressedMemo,
		"confirmed":  params.SegmentText,
	})
	resp, err := chat.Chat(ctx, llmprovider.ChatRequest{
		Model:           params.MemoryModel,
		ReasoningEffort: params.ReasoningEffort,
		Messages: []llmprovider.Message{
			{
				Role: llmprovider.RoleSystem,
				Content: params.Identity +
					fmt.Sprintf("\nFrom the confirmed segment being compacted, extract only value/principle candidates that guide judgment as a JSON string array []. "+
						"Do not turn a single transient impulse into dogma, and do not invent events. Return [] if none apply. Keep all candidates within %d tokens.", maxTokens),
			},
			{
				Role:    llmprovider.RoleUser,
				Content: string(payload),
			},
		},
	})
	if err != nil || resp == nil || len(resp.Choices) == 0 {
		return nil, err
	}
	raw := ExtractJSON(resp.Choices[0].Message.TextContent())
	var candidates []string
	if err := json.Unmarshal([]byte(raw), &candidates); err != nil {
		return nil, fmt.Errorf("memory: decode value candidates: %w", err)
	}
	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out, nil
}

// ValueModelResolver resolves the chat completer, consolidation model, reasoning effort, and character token budget.
type ValueModelResolver func(ctx context.Context, roomID, characterID string) (chat ChatCompleter, model string, effort string, ownBudget int, err error)

// ValueWorker polls value_jobs asynchronously and applies value consolidation
// without ever modifying committed messages.
type ValueWorker struct {
	rdb          storage.RDBStore
	resolve      ValueModelResolver
	pollInterval time.Duration
	onError      func(error)

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type ValueWorkerOption func(*ValueWorker)

func WithValuePollInterval(d time.Duration) ValueWorkerOption {
	return func(w *ValueWorker) {
		if d > 0 {
			w.pollInterval = d
		}
	}
}

func WithValueErrorHandler(fn func(error)) ValueWorkerOption {
	return func(w *ValueWorker) {
		w.onError = fn
	}
}

func NewValueWorker(rdb storage.RDBStore, resolve ValueModelResolver, opts ...ValueWorkerOption) *ValueWorker {
	w := &ValueWorker{
		rdb:          rdb,
		resolve:      resolve,
		pollInterval: 750 * time.Millisecond,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(w)
		}
	}
	return w
}

func (w *ValueWorker) Start(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		ticker := time.NewTicker(w.pollInterval)
		defer ticker.Stop()
		for {
			if _, err := w.ProcessOnce(runCtx); err != nil && runCtx.Err() == nil && w.onError != nil {
				w.onError(err)
			}
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (w *ValueWorker) Stop() {
	w.mu.Lock()
	cancel := w.cancel
	w.cancel = nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	w.wg.Wait()
}

// ProcessOnce claims and executes the next due queued value job, if any.
func (w *ValueWorker) ProcessOnce(ctx context.Context) (bool, error) {
	if w == nil || w.rdb == nil {
		return false, nil
	}
	job, err := w.rdb.Memories().ClaimNextValueJob(ctx, domain.NowMS())
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}
	if err := w.handleJob(ctx, *job); err != nil {
		nextStatus := domain.ValueJobQueued
		attempts := job.Attempts + 1
		if attempts >= maxValueJobAttempts {
			nextStatus = domain.ValueJobFailed
		}
		delay := time.Duration(1<<uint(min(attempts, 5))) * time.Second
		job.Status = nextStatus
		job.Attempts = attempts
		job.LastError = err.Error()
		job.NextRunAt = domain.NowMS() + delay.Milliseconds()
		_ = w.rdb.Memories().UpdateValueJob(ctx, *job)
		return true, err
	}
	return true, nil
}

func (w *ValueWorker) handleJob(ctx context.Context, job domain.ValueJob) error {
	if w.resolve == nil {
		return errors.New("memory: value worker resolver is nil")
	}
	chat, model, effort, ownBudget, err := w.resolve(ctx, job.RoomID, job.CharacterID)
	if err != nil {
		return err
	}
	if chat == nil || strings.TrimSpace(model) == "" {
		return errors.New("memory: no memory model configured for value consolidation")
	}
	if ownBudget <= 0 {
		ownBudget = 16384
	}

	var candidates []string
	if err := json.Unmarshal([]byte(job.CandidatesJSON), &candidates); err != nil {
		return fmt.Errorf("memory: decode candidates_json: %w", err)
	}
	if len(candidates) == 0 {
		job.Status = domain.ValueJobCompleted
		job.LastError = ""
		return w.rdb.Memories().UpdateValueJob(ctx, job)
	}

	members, err := w.rdb.Rooms().ListMembers(ctx, job.RoomID)
	if err != nil {
		return err
	}
	var member *domain.RoomMember
	for i := range members {
		if members[i].CharacterID == job.CharacterID {
			member = &members[i]
			break
		}
	}
	if member == nil {
		return fmt.Errorf("memory: character %q not found in room %q", job.CharacterID, job.RoomID)
	}
	if member.ActorState.ValuesRevision != job.BaseValuesRevision {
		return fmt.Errorf("memory: value revision conflict (job=%d, member=%d)", job.BaseValuesRevision, member.ActorState.ValuesRevision)
	}

	targetTokens := max(128, int(float64(ownBudget)*defaultValuesTargetRatio))
	maxTokens := max(256, int(float64(ownBudget)*defaultValuesMaxRatio))

	identity := buildValueIdentity(member.CharacterSnapshot)
	reqPayload, _ := json.Marshal(map[string]any{
		"values":     member.ActorState.Values,
		"candidates": candidates,
	})
	messages := []llmprovider.Message{
		{
			Role: llmprovider.RoleSystem,
			Content: identity +
				fmt.Sprintf("\nConsolidate the candidate values with existing values. Merge duplicates and refine conditions when warranted. "+
					"Return ONLY a JSON array of edits: [{\"id\":\"existing_id_or_null\",\"text\":\"new_or_updated_text_or_null\"}]. "+
					"Omit unchanged items. Target %d tokens and stay under %d tokens total.", targetTokens, maxTokens),
		},
		{
			Role:    llmprovider.RoleUser,
			Content: string(reqPayload),
		},
	}

	var nextValues []domain.CharacterValue
	var appliedEdits []ValueEdit
	var lastValidationErr error
	for attempt := 0; attempt < 3; attempt++ {
		if lastValidationErr != nil {
			messages = append(messages, llmprovider.Message{
				Role:    llmprovider.RoleUser,
				Content: lastValidationErr.Error(),
			})
		}
		resp, callErr := chat.Chat(ctx, llmprovider.ChatRequest{
			Model:           model,
			ReasoningEffort: effort,
			Messages:        messages,
		})
		if callErr != nil {
			return callErr
		}
		if resp == nil || len(resp.Choices) == 0 {
			return errors.New("memory: empty response from value consolidation model")
		}
		raw := ExtractJSON(resp.Choices[0].Message.TextContent())
		var edits []ValueEdit
		if err := json.Unmarshal([]byte(raw), &edits); err != nil {
			lastValidationErr = fmt.Errorf("Return a valid JSON array of {id, text} edits: %v", err)
			continue
		}
		updated, err := ApplyValueEdits(member.ActorState.Values, job.ID, edits)
		if err != nil {
			lastValidationErr = err
			continue
		}
		tokens := EstimateTokens(RenderValues(updated))
		if tokens > maxTokens {
			lastValidationErr = fmt.Errorf("Consolidated values use ~%d tokens, which exceeds limit %d; merge or prune further", tokens, maxTokens)
			continue
		}
		nextValues = updated
		appliedEdits = edits
		lastValidationErr = nil
		break
	}
	if lastValidationErr != nil {
		return lastValidationErr
	}

	return w.rdb.Tx(ctx, func(tx storage.RDBTx) error {
		txMembers, err := tx.Rooms().ListMembers(ctx, job.RoomID)
		if err != nil {
			return err
		}
		var current *domain.RoomMember
		for i := range txMembers {
			if txMembers[i].CharacterID == job.CharacterID {
				current = &txMembers[i]
				break
			}
		}
		if current == nil {
			return fmt.Errorf("memory: character %q disappeared from room %q", job.CharacterID, job.RoomID)
		}
		if current.ActorState.ValuesRevision != job.BaseValuesRevision {
			return fmt.Errorf("memory: concurrent value revision change (job=%d, current=%d)", job.BaseValuesRevision, current.ActorState.ValuesRevision)
		}

		current.ActorState.Values = nextValues
		current.ActorState.ValuesRevision++
		if err := tx.Rooms().UpsertMember(ctx, *current); err != nil {
			return err
		}

		histBytes, _ := json.Marshal(map[string]any{
			"revision": current.ActorState.ValuesRevision,
			"edits":    appliedEdits,
			"values":   nextValues,
		})
		nowMS := domain.NowMS()
		if err := tx.Memories().CreateMemory(ctx, domain.Memory{
			ID:           domain.NewID(),
			RoomID:       job.RoomID,
			OwnerID:      job.CharacterID,
			SourceTurnID: job.SourceTurnID,
			Kind:         domain.MemoryKindValueHistory,
			Content:      string(histBytes),
			ObservedBy:   []string{job.CharacterID},
			OccurredAt:   nowMS,
			Status:       domain.MemoryStatusActive,
			Revision:     current.ActorState.ValuesRevision,
			CreatedAt:    nowMS,
		}); err != nil {
			return err
		}

		job.Status = domain.ValueJobCompleted
		job.LastError = ""
		return tx.Memories().UpdateValueJob(ctx, job)
	})
}

func buildValueIdentity(c domain.Character) string {
	var b strings.Builder
	b.WriteString("Character: ")
	b.WriteString(c.Name)
	if c.Personality != "" {
		b.WriteString("\nPersonality: ")
		b.WriteString(c.Personality)
	}
	if c.Background != "" {
		b.WriteString("\nBackground: ")
		b.WriteString(c.Background)
	}
	return b.String()
}
