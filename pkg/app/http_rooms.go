package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/memory"
	"github.com/snowmerak/q/v2/pkg/runtime"
	"github.com/snowmerak/q/v2/pkg/storage"
)

type createRoomInput struct {
	Title                string               `json:"title"`
	Mode                 domain.RoomMode      `json:"mode"`
	PersonaID            string               `json:"persona_id"`
	CharacterIDs         []string             `json:"character_ids"`
	ApprovalModeOverride *domain.ApprovalMode `json:"approval_mode_override,omitempty"`
	ModelOverrides       *domain.ModelPrefs   `json:"model_overrides,omitempty"`
	MaxTurnsPerInput     int                  `json:"max_turns_per_input,omitempty"`
	WorkDir              string               `json:"work_dir,omitempty"`
}

type patchRoomInput struct {
	ExpectedVersion      *int64               `json:"expected_version"`
	Title                *string              `json:"title,omitempty"`
	CharacterIDs         []string             `json:"character_ids,omitempty"`
	ApprovalModeOverride *domain.ApprovalMode `json:"approval_mode_override,omitempty"`
	ModelOverrides       *domain.ModelPrefs   `json:"model_overrides,omitempty"`
	MaxTurnsPerInput     *int                 `json:"max_turns_per_input,omitempty"`
	WorkDir              *string              `json:"work_dir,omitempty"`
}

type roomDetailResponse struct {
	Room                  domain.Room             `json:"room"`
	Members               []domain.RoomMember     `json:"members"`
	Turns                 []domain.Turn           `json:"turns"`
	PendingApprovals      []domain.ToolInvocation `json:"pending_approvals"`
	PendingQuestions      []domain.UserQuestion   `json:"pending_questions"`
	ActiveToolInvocations []domain.ToolInvocation `json:"active_tool_invocations"`
	ActiveTurn            bool                    `json:"active_turn"`
}

func (a *App) handleListRooms(w http.ResponseWriter, r *http.Request) {
	rooms, err := a.rdb.Rooms().List(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	type roomSummary struct {
		Room    domain.Room         `json:"room"`
		Members []domain.RoomMember `json:"members"`
	}
	items := make([]roomSummary, 0, len(rooms))
	for _, rm := range rooms {
		members, err := a.rdb.Rooms().ListMembers(r.Context(), rm.ID)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		if members == nil {
			members = []domain.RoomMember{}
		}
		items = append(items, roomSummary{Room: rm, Members: members})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *App) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	var in createRoomInput
	if !decodeJSONBody(w, r, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "room title is required", false, nil)
		return
	}
	if in.Mode == "" {
		in.Mode = domain.RoomModeDirect
	}
	if !in.Mode.Valid() {
		writeError(w, http.StatusBadRequest, "invalid_input", "invalid room mode", false, nil)
		return
	}
	if in.Mode == domain.RoomModeDirect && len(in.CharacterIDs) != 1 {
		writeError(w, http.StatusBadRequest, "invalid_input", "direct room requires exactly 1 character_id", false, nil)
		return
	}
	if in.Mode == domain.RoomModeGroup && len(in.CharacterIDs) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_input", "group room requires at least 1 character_id", false, nil)
		return
	}
	if in.ApprovalModeOverride != nil && !in.ApprovalModeOverride.Valid() {
		writeError(w, http.StatusBadRequest, "invalid_input", "invalid approval_mode_override", false, nil)
		return
	}

	ctx := r.Context()
	persona, err := a.rdb.Personas().Get(ctx, strings.TrimSpace(in.PersonaID))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "persona not found", false, nil)
			return
		}
		writeDomainError(w, err)
		return
	}

	roomDraft := domain.Room{
		Title:                in.Title,
		Mode:                 in.Mode,
		PersonaID:            persona.ID,
		PersonaSnapshot:      persona,
		ApprovalModeOverride: in.ApprovalModeOverride,
		ModelOverrides:       in.ModelOverrides,
		MaxTurnsPerInput:     in.MaxTurnsPerInput,
		WorkDir:              strings.TrimSpace(in.WorkDir),
	}

	snap := a.Snapshot()
	defer snap.Close()
	nowMS := domain.NowMS()
	members := make([]domain.RoomMember, 0, len(in.CharacterIDs))
	seenChars := make(map[string]struct{}, len(in.CharacterIDs))
	for _, cid := range in.CharacterIDs {
		cid = strings.TrimSpace(cid)
		if cid == "" {
			writeError(w, http.StatusBadRequest, "invalid_input", "character_ids cannot contain empty strings", false, nil)
			return
		}
		if _, dup := seenChars[cid]; dup {
			writeError(w, http.StatusBadRequest, "invalid_input", "duplicate character_id "+cid, false, nil)
			return
		}
		seenChars[cid] = struct{}{}

		ch, err := a.rdb.Characters().Get(ctx, cid)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "character "+cid+" not found", false, nil)
				return
			}
			writeDomainError(w, err)
			return
		}

		state := seedActorState(ch, nowMS)
		var lookup memory.ModelLookup
		if snap.Gateway != nil {
			lookup = snap.Gateway
		}
		models := runtime.ResolveModels(ctx, snap.Config, roomDraft, ch, lookup)
		if err := memory.AssertValuesBudget(state.Values, models.ContextWindow, 0.20); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "values_too_large", err.Error(), false, nil)
			return
		}

		members = append(members, domain.RoomMember{
			CharacterID:       ch.ID,
			CharacterSnapshot: ch,
			BaseSpeed:         10,
			ActorState:        state,
			Active:            true,
			JoinedAt:          nowMS,
		})
	}

	created, err := a.rdb.Rooms().Create(ctx, roomDraft, members)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	detail, err := a.loadRoomDetail(ctx, created.ID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, detail)
}

func (a *App) handleGetRoom(w http.ResponseWriter, r *http.Request) {
	detail, err := a.loadRoomDetail(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *App) handlePatchRoom(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.engine.Hub().HasActiveTurn(id) {
		writeError(w, http.StatusConflict, "session_busy", "cannot update room while a turn is running", true, nil)
		return
	}
	var patch patchRoomInput
	if !decodeJSONBody(w, r, &patch) {
		return
	}
	if patch.ExpectedVersion == nil || *patch.ExpectedVersion < 1 {
		writeError(w, http.StatusBadRequest, "invalid_input", "expected_version >= 1 is required", false, nil)
		return
	}
	if patch.ApprovalModeOverride != nil && !patch.ApprovalModeOverride.Valid() {
		writeError(w, http.StatusBadRequest, "invalid_input", "invalid approval_mode_override", false, nil)
		return
	}

	var cleanCharIDs []string
	if patch.CharacterIDs != nil {
		cleanCharIDs = make([]string, 0, len(patch.CharacterIDs))
		seen := make(map[string]struct{}, len(patch.CharacterIDs))
		for _, rawID := range patch.CharacterIDs {
			cid := strings.TrimSpace(rawID)
			if cid == "" {
				writeError(w, http.StatusBadRequest, "invalid_input", "character_ids cannot contain empty strings", false, nil)
				return
			}
			if _, dup := seen[cid]; dup {
				continue
			}
			seen[cid] = struct{}{}
			cleanCharIDs = append(cleanCharIDs, cid)
		}
	}

	type apiErr struct {
		status int
		code   string
		msg    string
	}
	var validationErr *apiErr

	ctx := r.Context()
	txErr := a.rdb.Tx(ctx, func(tx storage.RDBTx) error {
		cur, err := tx.Rooms().Get(ctx, id)
		if err != nil {
			return err
		}
		if cur.Version != *patch.ExpectedVersion {
			validationErr = &apiErr{status: http.StatusConflict, code: "version_conflict", msg: "room version mismatch"}
			return errors.New("version_conflict")
		}
		if patch.Title != nil {
			title := strings.TrimSpace(*patch.Title)
			if title == "" {
				validationErr = &apiErr{status: http.StatusBadRequest, code: "invalid_input", msg: "room title cannot be empty"}
				return errors.New("invalid_input")
			}
			cur.Title = title
		}
		if patch.ApprovalModeOverride != nil {
			cur.ApprovalModeOverride = patch.ApprovalModeOverride
		}
		if patch.ModelOverrides != nil {
			cur.ModelOverrides = patch.ModelOverrides
		}
		if patch.MaxTurnsPerInput != nil && *patch.MaxTurnsPerInput > 0 {
			cur.MaxTurnsPerInput = *patch.MaxTurnsPerInput
		}
		if patch.WorkDir != nil {
			cur.WorkDir = strings.TrimSpace(*patch.WorkDir)
		}

		var existing []domain.RoomMember
		fetchedChars := make(map[string]domain.Character)
		if patch.CharacterIDs != nil {
			if cur.Mode == domain.RoomModeDirect && len(cleanCharIDs) != 1 {
				validationErr = &apiErr{status: http.StatusBadRequest, code: "invalid_input", msg: "direct room requires exactly 1 character_id"}
				return errors.New("invalid_input")
			}
			if cur.Mode == domain.RoomModeGroup && len(cleanCharIDs) == 0 {
				validationErr = &apiErr{status: http.StatusBadRequest, code: "invalid_input", msg: "group room requires at least 1 character_id"}
				return errors.New("invalid_input")
			}
			existing, err = tx.Rooms().ListMembers(ctx, id)
			if err != nil {
				return err
			}
			existingSet := make(map[string]struct{}, len(existing))
			for _, m := range existing {
				existingSet[m.CharacterID] = struct{}{}
			}
			for _, cid := range cleanCharIDs {
				if _, ok := existingSet[cid]; ok {
					continue
				}
				ch, err := tx.Characters().Get(ctx, cid)
				if err != nil {
					if errors.Is(err, storage.ErrNotFound) {
						validationErr = &apiErr{status: http.StatusNotFound, code: "not_found", msg: "character " + cid + " not found"}
					}
					return err
				}
				fetchedChars[cid] = ch
			}
		}

		updated, err := tx.Rooms().Update(ctx, cur)
		if err != nil {
			return err
		}

		if patch.CharacterIDs != nil {
			existingByID := make(map[string]domain.RoomMember, len(existing))
			for _, m := range existing {
				existingByID[m.CharacterID] = m
			}
			wanted := make(map[string]struct{}, len(cleanCharIDs))
			nowMS := domain.NowMS()
			for _, cid := range cleanCharIDs {
				wanted[cid] = struct{}{}
				if m, ok := existingByID[cid]; ok {
					m.Active = true
					if err := tx.Rooms().UpsertMember(ctx, m); err != nil {
						return err
					}
					continue
				}
				ch := fetchedChars[cid]
				if err := tx.Rooms().UpsertMember(ctx, domain.RoomMember{
					RoomID:            updated.ID,
					CharacterID:       ch.ID,
					CharacterSnapshot: ch,
					BaseSpeed:         10,
					ActorState:        seedActorState(ch, nowMS),
					Active:            true,
					JoinedAt:          nowMS,
				}); err != nil {
					return err
				}
			}
			for _, m := range existing {
				if _, keep := wanted[m.CharacterID]; !keep && m.Active {
					m.Active = false
					if err := tx.Rooms().UpsertMember(ctx, m); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if validationErr != nil {
		writeError(w, validationErr.status, validationErr.code, validationErr.msg, false, nil)
		return
	}
	if txErr != nil {
		writeDomainError(w, txErr)
		return
	}

	detail, err := a.loadRoomDetail(ctx, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *App) handleDeleteRoom(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.engine.Hub().HasActiveTurn(id) {
		a.engine.Intercept(id, "")
	}
	if err := a.rdb.Rooms().Delete(r.Context(), id, domain.NowMS()); err != nil {
		writeDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type turnActorKey struct {
	turnID  string
	actorID string
}

func compactedSeqByActor(members []domain.RoomMember) (map[string]int64, int64) {
	byActor := make(map[string]int64, len(members))
	var minActive int64
	hasActive := false
	for _, m := range members {
		byActor[m.CharacterID] = m.ActorState.CompactedThroughSeq
		if !m.Active {
			continue
		}
		if !hasActive || m.ActorState.CompactedThroughSeq < minActive {
			minActive = m.ActorState.CompactedThroughSeq
			hasActive = true
		}
	}
	if !hasActive {
		minActive = 0
	}
	return byActor, minActive
}

func isMessageCompacted(m domain.Message, byActor map[string]int64, minActive int64) bool {
	if m.Role == domain.MessageRoleCharacter {
		if seq, ok := byActor[m.AuthorID]; ok {
			return seq > 0 && m.Seq <= seq
		}
	}
	return minActive > 0 && m.Seq <= minActive
}

func stripThinkingFromMetadata(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return raw
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil || obj == nil {
		return raw
	}
	if _, exists := obj["thinking"]; !exists {
		return raw
	}
	delete(obj, "thinking")
	if len(obj) == 0 {
		return ""
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return string(b)
}

func (a *App) handleListMessages(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	if _, err := a.rdb.Rooms().Get(r.Context(), roomID); err != nil {
		writeDomainError(w, err)
		return
	}

	var beforeSeq int64
	if raw := strings.TrimSpace(r.URL.Query().Get("before_seq")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			writeError(w, http.StatusBadRequest, "invalid_input", "invalid before_seq", false, nil)
			return
		}
		beforeSeq = v
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_input", "invalid limit", false, nil)
			return
		}
		if v > 200 {
			v = 200
		}
		limit = v
	}

	msgs, err := a.rdb.Messages().List(r.Context(), roomID, beforeSeq, limit+1)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	members, err := a.rdb.Rooms().ListMembers(r.Context(), roomID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var nextBeforeSeq *int64
	if len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:]
		next := msgs[0].Seq
		nextBeforeSeq = &next
	}
	if msgs == nil {
		msgs = []domain.Message{}
	}

	byActor, minActive := compactedSeqByActor(members)
	compactedTurnActors := make(map[turnActorKey]bool)
	uncompactedTurnActors := make(map[turnActorKey]bool)
	seenTurn := make(map[string]struct{})
	uncompactedTurns := make([]string, 0)

	for i := range msgs {
		msg := msgs[i]
		compacted := isMessageCompacted(msg, byActor, minActive)
		if compacted {
			msgs[i].MetadataJSON = stripThinkingFromMetadata(msg.MetadataJSON)
			if msg.Role == domain.MessageRoleCharacter && msg.TurnID != "" {
				compactedTurnActors[turnActorKey{turnID: msg.TurnID, actorID: msg.AuthorID}] = true
			}
			continue
		}
		if msg.Role == domain.MessageRoleCharacter && msg.TurnID != "" {
			uncompactedTurnActors[turnActorKey{turnID: msg.TurnID, actorID: msg.AuthorID}] = true
		}
		if msg.TurnID != "" {
			if _, ok := seenTurn[msg.TurnID]; !ok {
				seenTurn[msg.TurnID] = struct{}{}
				uncompactedTurns = append(uncompactedTurns, msg.TurnID)
			}
		}
	}

	toolInvs := make([]domain.ToolInvocation, 0)
	for _, turnID := range uncompactedTurns {
		invs, err := a.rdb.Tools().ListInvocationsByTurn(r.Context(), turnID)
		if err != nil {
			continue
		}
		for _, inv := range invs {
			key := turnActorKey{turnID: inv.TurnID, actorID: inv.ActorID}
			if compactedTurnActors[key] && !uncompactedTurnActors[key] {
				continue
			}
			toolInvs = append(toolInvs, inv)
		}
	}
	slices.SortFunc(toolInvs, func(left, right domain.ToolInvocation) int {
		if c := cmp.Compare(left.CreatedAt, right.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(left.ID, right.ID)
	})

	resp := map[string]any{
		"items":            msgs,
		"tool_invocations": toolInvs,
	}
	if nextBeforeSeq != nil {
		resp["next_before_seq"] = *nextBeforeSeq
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) handleSubmitTurn(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	var req runtime.SubmitTurnRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	res, err := a.engine.SubmitTurn(r.Context(), roomID, req)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	status := http.StatusAccepted
	if res.AlreadyCompleted {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"turn_id":         res.Turn.ID,
		"status":          res.Turn.Status,
		"user_message_id": res.UserMessageID,
	})
}

func (a *App) handleRetryTurn(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	turnID := r.PathValue("turnId")
	turn, err := a.engine.RetryTurn(r.Context(), roomID, turnID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"turn_id": turn.ID,
		"status":  turn.Status,
	})
}

func (a *App) handleInterceptTurn(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	if _, err := a.rdb.Rooms().Get(r.Context(), roomID); err != nil {
		writeDomainError(w, err)
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	accepted := a.engine.Intercept(roomID, req.Text)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": accepted,
	})
}

func (a *App) handleRoomStream(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	if _, err := a.rdb.Rooms().Get(r.Context(), roomID); err != nil {
		writeDomainError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming is unsupported by this HTTP writer", false, nil)
		return
	}

	var lastID int64
	if raw := strings.TrimSpace(r.Header.Get("Last-Event-ID")); raw != "" {
		lastID, _ = strconv.ParseInt(raw, 10, 64)
	}

	evCh, unsub := a.engine.Events().Subscribe(roomID, lastID)
	defer unsub()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case ev, ok := <-evCh:
			if !ok {
				return
			}
			data, err := json.Marshal(ev.Data)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, data)
			flusher.Flush()
		}
	}
}

func (a *App) handleRoomMemories(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	if _, err := a.rdb.Rooms().Get(r.Context(), roomID); err != nil {
		writeDomainError(w, err)
		return
	}
	mems, err := a.rdb.Memories().ListMemories(r.Context(), roomID, "", "", 500)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	members, err := a.rdb.Rooms().ListMembers(r.Context(), roomID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if mems == nil {
		mems = []domain.Memory{}
	}
	valuesByActor := make(map[string][]domain.CharacterValue, len(members))
	for _, m := range members {
		valuesByActor[m.CharacterID] = m.ActorState.Values
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"memories": mems,
		"values":   valuesByActor,
	})
}

func (a *App) loadRoomDetail(ctx context.Context, roomID string) (roomDetailResponse, error) {
	rm, err := a.rdb.Rooms().Get(ctx, roomID)
	if err != nil {
		return roomDetailResponse{}, err
	}
	members, err := a.rdb.Rooms().ListMembers(ctx, roomID)
	if err != nil {
		return roomDetailResponse{}, err
	}
	if members == nil {
		members = []domain.RoomMember{}
	}
	turns, err := a.rdb.Turns().ListByRoom(ctx, roomID, 20)
	if err != nil {
		return roomDetailResponse{}, err
	}
	if turns == nil {
		turns = []domain.Turn{}
	}

	pendingApprovals := make([]domain.ToolInvocation, 0)
	pendingQuestions := make([]domain.UserQuestion, 0)
	activeToolInvocations := make([]domain.ToolInvocation, 0)
	for _, t := range turns {
		if t.Status != domain.TurnStatusAwaitingApproval && t.Status != domain.TurnStatusAwaitingUser && t.Status != domain.TurnStatusRunning {
			continue
		}
		invs, _ := a.rdb.Tools().ListInvocationsByTurn(ctx, t.ID)
		activeToolInvocations = append(activeToolInvocations, invs...)
		for _, inv := range invs {
			if inv.Status == domain.ToolStatusPendingApproval {
				pendingApprovals = append(pendingApprovals, inv)
			}
		}
		qs, _ := a.rdb.Tools().ListQuestionsByTurn(ctx, t.ID)
		for _, q := range qs {
			if q.Status == domain.QuestionStatusPending {
				pendingQuestions = append(pendingQuestions, q)
			}
		}
	}
	slices.SortFunc(activeToolInvocations, func(left, right domain.ToolInvocation) int {
		if c := cmp.Compare(left.CreatedAt, right.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(left.ID, right.ID)
	})

	return roomDetailResponse{
		Room:                  rm,
		Members:               members,
		Turns:                 turns,
		PendingApprovals:      pendingApprovals,
		PendingQuestions:      pendingQuestions,
		ActiveToolInvocations: activeToolInvocations,
		ActiveTurn:            a.engine.Hub().HasActiveTurn(roomID),
	}, nil
}

func seedActorState(ch domain.Character, nowMS int64) domain.ActorState {
	var state domain.ActorState
	if ch.InitialState != nil {
		if ch.InitialState.Self != nil {
			state.Self = *ch.InitialState.Self
			state.StateUpdatedAt = nowMS
		}
		if len(ch.InitialState.Relationships) > 0 {
			state.Relationships = maps.Clone(ch.InitialState.Relationships)
			state.RelationshipsUpdatedAt = make(map[string]int64, len(ch.InitialState.Relationships))
			for k := range ch.InitialState.Relationships {
				state.RelationshipsUpdatedAt[k] = nowMS
			}
		}
	}
	if len(ch.InitialValues) > 0 {
		vals := make([]domain.CharacterValue, len(ch.InitialValues))
		for i, text := range ch.InitialValues {
			vals[i] = domain.CharacterValue{
				ID:   fmt.Sprintf("v-initial-%d", i+1),
				Text: text,
			}
		}
		state.Values = vals
		state.ValuesRevision = 1
	}
	return state
}
