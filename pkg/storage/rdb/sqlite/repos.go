package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/storage"
)

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func intToBool(i int) bool { return i != 0 }

func marshalJSONString(v any, empty string) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if string(data) == "null" {
		return empty, nil
	}
	return string(data), nil
}

func encodeFloat32s(vec []float32) []byte {
	if len(vec) == 0 {
		return nil
	}
	buf := make([]byte, len(vec)*4)
	for i, f := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func decodeFloat32s(buf []byte) []float32 {
	if len(buf) == 0 || len(buf)%4 != 0 {
		return nil
	}
	out := make([]float32, len(buf)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return out
}

func mapSQLiteErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ErrNotFound
	}
	msg := err.Error()
	if strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "PRIMARY KEY") {
		return fmt.Errorf("%w: %v", storage.ErrAlreadyExists, err)
	}
	return err
}

// --- Personas ---

type personaRepo struct{ db dbExec }

func (r *personaRepo) Create(ctx context.Context, p domain.Persona) (domain.Persona, error) {
	if p.ID == "" {
		p.ID = domain.NewID()
	}
	if p.Version == 0 {
		p.Version = 1
	}
	now := domain.NowMS()
	if p.CreatedAt == 0 {
		p.CreatedAt = now
	}
	if p.UpdatedAt == 0 {
		p.UpdatedAt = p.CreatedAt
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO personas (id, name, description, avatar_artifact_id, is_default, version, created_at, updated_at, deleted_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Description, p.AvatarArtifactID, boolToInt(p.IsDefault), p.Version, p.CreatedAt, p.UpdatedAt, p.DeletedAt,
	)
	if err != nil {
		return domain.Persona{}, mapSQLiteErr(err)
	}
	return p, nil
}

func (r *personaRepo) Get(ctx context.Context, id string) (domain.Persona, error) {
	var p domain.Persona
	var isDef int
	var deletedAt sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, description, avatar_artifact_id, is_default, version, created_at, updated_at, deleted_at
		 FROM personas WHERE id = ? AND deleted_at IS NULL`, id,
	).Scan(&p.ID, &p.Name, &p.Description, &p.AvatarArtifactID, &isDef, &p.Version, &p.CreatedAt, &p.UpdatedAt, &deletedAt)
	if err != nil {
		return domain.Persona{}, mapSQLiteErr(err)
	}
	p.IsDefault = intToBool(isDef)
	if deletedAt.Valid {
		v := deletedAt.Int64
		p.DeletedAt = &v
	}
	return p, nil
}

func (r *personaRepo) List(ctx context.Context) ([]domain.Persona, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, description, avatar_artifact_id, is_default, version, created_at, updated_at
		 FROM personas WHERE deleted_at IS NULL ORDER BY created_at ASC, id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Persona
	for rows.Next() {
		var p domain.Persona
		var isDef int
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.AvatarArtifactID, &isDef, &p.Version, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.IsDefault = intToBool(isDef)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *personaRepo) Update(ctx context.Context, p domain.Persona) (domain.Persona, error) {
	expected := p.Version
	p.Version++
	p.UpdatedAt = domain.NowMS()
	res, err := r.db.ExecContext(ctx,
		`UPDATE personas SET name = ?, description = ?, avatar_artifact_id = ?, is_default = ?, version = ?, updated_at = ?
		 WHERE id = ? AND version = ? AND deleted_at IS NULL`,
		p.Name, p.Description, p.AvatarArtifactID, boolToInt(p.IsDefault), p.Version, p.UpdatedAt, p.ID, expected,
	)
	if err != nil {
		return domain.Persona{}, mapSQLiteErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		if _, err := r.Get(ctx, p.ID); errors.Is(err, storage.ErrNotFound) {
			return domain.Persona{}, storage.ErrNotFound
		}
		return domain.Persona{}, storage.ErrConflict
	}
	return p, nil
}

func (r *personaRepo) Delete(ctx context.Context, id string, deletedAt int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE personas SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		deletedAt, deletedAt, id,
	)
	return err
}

// --- Characters ---

type characterRepo struct{ db dbExec }

func (r *characterRepo) Create(ctx context.Context, c domain.Character) (domain.Character, error) {
	if c.ID == "" {
		c.ID = domain.NewID()
	}
	if c.Version == 0 {
		c.Version = 1
	}
	now := domain.NowMS()
	if c.CreatedAt == 0 {
		c.CreatedAt = now
	}
	if c.UpdatedAt == 0 {
		c.UpdatedAt = c.CreatedAt
	}
	initVals, err := marshalJSONString(c.InitialValues, "[]")
	if err != nil {
		return domain.Character{}, err
	}
	initState, err := marshalJSONString(c.InitialState, "{}")
	if err != nil {
		return domain.Character{}, err
	}
	modelPrefs, err := marshalJSONString(c.ModelPrefs, "{}")
	if err != nil {
		return domain.Character{}, err
	}
	var override string
	if c.ApprovalPolicyOverride != nil {
		override = string(*c.ApprovalPolicyOverride)
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO characters (id, name, avatar_artifact_id, personality, background, appearance, initial_values_json, initial_state_json, model_prefs_json, approval_policy_override, version, created_at, updated_at, deleted_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.AvatarArtifactID, c.Personality, c.Background, c.Appearance, initVals, initState, modelPrefs, override, c.Version, c.CreatedAt, c.UpdatedAt, c.DeletedAt,
	)
	if err != nil {
		return domain.Character{}, mapSQLiteErr(err)
	}
	return c, nil
}

func scanCharacter(scan func(dest ...any) error) (domain.Character, error) {
	var c domain.Character
	var initVals, initState, modelPrefs, override string
	var deletedAt sql.NullInt64
	if err := scan(&c.ID, &c.Name, &c.AvatarArtifactID, &c.Personality, &c.Background, &c.Appearance, &initVals, &initState, &modelPrefs, &override, &c.Version, &c.CreatedAt, &c.UpdatedAt, &deletedAt); err != nil {
		return domain.Character{}, mapSQLiteErr(err)
	}
	if initVals != "" && initVals != "[]" {
		_ = json.Unmarshal([]byte(initVals), &c.InitialValues)
	}
	if initState != "" && initState != "{}" {
		var st domain.InitialState
		if json.Unmarshal([]byte(initState), &st) == nil {
			c.InitialState = &st
		}
	}
	if modelPrefs != "" && modelPrefs != "{}" {
		var mp domain.ModelPrefs
		if json.Unmarshal([]byte(modelPrefs), &mp) == nil {
			c.ModelPrefs = &mp
		}
	}
	if override != "" {
		m := domain.ApprovalMode(override)
		c.ApprovalPolicyOverride = &m
	}
	if deletedAt.Valid {
		v := deletedAt.Int64
		c.DeletedAt = &v
	}
	return c, nil
}

func (r *characterRepo) Get(ctx context.Context, id string) (domain.Character, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, name, avatar_artifact_id, personality, background, appearance, initial_values_json, initial_state_json, model_prefs_json, approval_policy_override, version, created_at, updated_at, deleted_at
		 FROM characters WHERE id = ? AND deleted_at IS NULL`, id,
	)
	return scanCharacter(row.Scan)
}

func (r *characterRepo) List(ctx context.Context) ([]domain.Character, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, avatar_artifact_id, personality, background, appearance, initial_values_json, initial_state_json, model_prefs_json, approval_policy_override, version, created_at, updated_at, deleted_at
		 FROM characters WHERE deleted_at IS NULL ORDER BY created_at ASC, id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Character
	for rows.Next() {
		c, err := scanCharacter(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *characterRepo) Update(ctx context.Context, c domain.Character) (domain.Character, error) {
	expected := c.Version
	c.Version++
	c.UpdatedAt = domain.NowMS()
	initVals, err := marshalJSONString(c.InitialValues, "[]")
	if err != nil {
		return domain.Character{}, err
	}
	initState, err := marshalJSONString(c.InitialState, "{}")
	if err != nil {
		return domain.Character{}, err
	}
	modelPrefs, err := marshalJSONString(c.ModelPrefs, "{}")
	if err != nil {
		return domain.Character{}, err
	}
	var override string
	if c.ApprovalPolicyOverride != nil {
		override = string(*c.ApprovalPolicyOverride)
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE characters SET name = ?, avatar_artifact_id = ?, personality = ?, background = ?, appearance = ?, initial_values_json = ?, initial_state_json = ?, model_prefs_json = ?, approval_policy_override = ?, version = ?, updated_at = ?
		 WHERE id = ? AND version = ? AND deleted_at IS NULL`,
		c.Name, c.AvatarArtifactID, c.Personality, c.Background, c.Appearance, initVals, initState, modelPrefs, override, c.Version, c.UpdatedAt, c.ID, expected,
	)
	if err != nil {
		return domain.Character{}, mapSQLiteErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		if _, err := r.Get(ctx, c.ID); errors.Is(err, storage.ErrNotFound) {
			return domain.Character{}, storage.ErrNotFound
		}
		return domain.Character{}, storage.ErrConflict
	}
	return c, nil
}

func (r *characterRepo) Delete(ctx context.Context, id string, deletedAt int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE characters SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		deletedAt, deletedAt, id,
	)
	return err
}

// --- Rooms & Members ---

type roomRepo struct{ db dbExec }

func (r *roomRepo) Create(ctx context.Context, rm domain.Room, members []domain.RoomMember) (domain.Room, error) {
	if rm.ID == "" {
		rm.ID = domain.NewID()
	}
	if rm.Version == 0 {
		rm.Version = 1
	}
	if rm.ContextVersion == 0 {
		rm.ContextVersion = 1
	}
	if rm.MaxTurnsPerInput == 0 {
		rm.MaxTurnsPerInput = 6
	}
	now := domain.NowMS()
	if rm.CreatedAt == 0 {
		rm.CreatedAt = now
	}
	if rm.UpdatedAt == 0 {
		rm.UpdatedAt = rm.CreatedAt
	}
	personaSnap, err := marshalJSONString(rm.PersonaSnapshot, "{}")
	if err != nil {
		return domain.Room{}, err
	}
	modelOv, err := marshalJSONString(rm.ModelOverrides, "{}")
	if err != nil {
		return domain.Room{}, err
	}
	var approvalOv string
	if rm.ApprovalModeOverride != nil {
		approvalOv = string(*rm.ApprovalModeOverride)
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO rooms (id, title, mode, persona_id, persona_snapshot_json, approval_mode_override, model_overrides_json, max_turns_per_input, context_version, version, created_at, updated_at, deleted_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rm.ID, rm.Title, string(rm.Mode), rm.PersonaID, personaSnap, approvalOv, modelOv, rm.MaxTurnsPerInput, rm.ContextVersion, rm.Version, rm.CreatedAt, rm.UpdatedAt, rm.DeletedAt,
	)
	if err != nil {
		return domain.Room{}, mapSQLiteErr(err)
	}
	for _, m := range members {
		m.RoomID = rm.ID
		if m.JoinedAt == 0 {
			m.JoinedAt = rm.CreatedAt
		}
		if err := r.UpsertMember(ctx, m); err != nil {
			return domain.Room{}, err
		}
	}
	return rm, nil
}

func scanRoom(scan func(dest ...any) error) (domain.Room, error) {
	var rm domain.Room
	var mode, personaSnap, approvalOv, modelOv string
	var deletedAt sql.NullInt64
	if err := scan(&rm.ID, &rm.Title, &mode, &rm.PersonaID, &personaSnap, &approvalOv, &modelOv, &rm.MaxTurnsPerInput, &rm.ContextVersion, &rm.Version, &rm.CreatedAt, &rm.UpdatedAt, &deletedAt); err != nil {
		return domain.Room{}, mapSQLiteErr(err)
	}
	rm.Mode = domain.RoomMode(mode)
	_ = json.Unmarshal([]byte(personaSnap), &rm.PersonaSnapshot)
	if approvalOv != "" {
		m := domain.ApprovalMode(approvalOv)
		rm.ApprovalModeOverride = &m
	}
	if modelOv != "" && modelOv != "{}" {
		var mp domain.ModelPrefs
		if json.Unmarshal([]byte(modelOv), &mp) == nil {
			rm.ModelOverrides = &mp
		}
	}
	if deletedAt.Valid {
		v := deletedAt.Int64
		rm.DeletedAt = &v
	}
	return rm, nil
}

func (r *roomRepo) Get(ctx context.Context, id string) (domain.Room, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, title, mode, persona_id, persona_snapshot_json, approval_mode_override, model_overrides_json, max_turns_per_input, context_version, version, created_at, updated_at, deleted_at
		 FROM rooms WHERE id = ? AND deleted_at IS NULL`, id,
	)
	return scanRoom(row.Scan)
}

func (r *roomRepo) List(ctx context.Context) ([]domain.Room, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, title, mode, persona_id, persona_snapshot_json, approval_mode_override, model_overrides_json, max_turns_per_input, context_version, version, created_at, updated_at, deleted_at
		 FROM rooms WHERE deleted_at IS NULL ORDER BY updated_at DESC, id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Room
	for rows.Next() {
		rm, err := scanRoom(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, rm)
	}
	return out, rows.Err()
}

func (r *roomRepo) Update(ctx context.Context, rm domain.Room) (domain.Room, error) {
	expected := rm.Version
	rm.Version++
	rm.UpdatedAt = domain.NowMS()
	personaSnap, err := marshalJSONString(rm.PersonaSnapshot, "{}")
	if err != nil {
		return domain.Room{}, err
	}
	modelOv, err := marshalJSONString(rm.ModelOverrides, "{}")
	if err != nil {
		return domain.Room{}, err
	}
	var approvalOv string
	if rm.ApprovalModeOverride != nil {
		approvalOv = string(*rm.ApprovalModeOverride)
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE rooms SET title = ?, persona_snapshot_json = ?, approval_mode_override = ?, model_overrides_json = ?, max_turns_per_input = ?, context_version = ?, version = ?, updated_at = ?
		 WHERE id = ? AND version = ? AND deleted_at IS NULL`,
		rm.Title, personaSnap, approvalOv, modelOv, rm.MaxTurnsPerInput, rm.ContextVersion, rm.Version, rm.UpdatedAt, rm.ID, expected,
	)
	if err != nil {
		return domain.Room{}, mapSQLiteErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		if _, err := r.Get(ctx, rm.ID); errors.Is(err, storage.ErrNotFound) {
			return domain.Room{}, storage.ErrNotFound
		}
		return domain.Room{}, storage.ErrConflict
	}
	return rm, nil
}

func (r *roomRepo) Delete(ctx context.Context, id string, deletedAt int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE rooms SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		deletedAt, deletedAt, id,
	)
	return err
}

func (r *roomRepo) UpsertMember(ctx context.Context, m domain.RoomMember) error {
	if m.BaseSpeed == 0 {
		m.BaseSpeed = 10
	}
	if m.JoinedAt == 0 {
		m.JoinedAt = domain.NowMS()
	}
	charSnap, err := marshalJSONString(m.CharacterSnapshot, "{}")
	if err != nil {
		return err
	}
	actorState, err := marshalJSONString(m.ActorState, "{}")
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO room_members (room_id, character_id, character_snapshot_json, base_speed, actor_state_json, active, joined_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(room_id, character_id) DO UPDATE SET
		   character_snapshot_json = excluded.character_snapshot_json,
		   base_speed = excluded.base_speed,
		   actor_state_json = excluded.actor_state_json,
		   active = excluded.active`,
		m.RoomID, m.CharacterID, charSnap, m.BaseSpeed, actorState, boolToInt(m.Active), m.JoinedAt,
	)
	return mapSQLiteErr(err)
}

func (r *roomRepo) ListMembers(ctx context.Context, roomID string) ([]domain.RoomMember, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT room_id, character_id, character_snapshot_json, base_speed, actor_state_json, active, joined_at
		 FROM room_members WHERE room_id = ? ORDER BY joined_at ASC, character_id ASC`, roomID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.RoomMember
	for rows.Next() {
		var m domain.RoomMember
		var charSnap, actorState string
		var active int
		if err := rows.Scan(&m.RoomID, &m.CharacterID, &charSnap, &m.BaseSpeed, &actorState, &active, &m.JoinedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(charSnap), &m.CharacterSnapshot)
		_ = json.Unmarshal([]byte(actorState), &m.ActorState)
		m.Active = intToBool(active)
		out = append(out, m)
	}
	return out, rows.Err()
}

// --- Turns ---

type turnRepo struct{ db dbExec }

func (r *turnRepo) CreateOrGetIdempotent(ctx context.Context, t domain.Turn) (domain.Turn, bool, error) {
	if t.ID == "" {
		t.ID = domain.NewID()
	}
	now := domain.NowMS()
	if t.CreatedAt == 0 {
		t.CreatedAt = now
	}
	if t.UpdatedAt == 0 {
		t.UpdatedAt = t.CreatedAt
	}
	if t.CheckpointJSON == "" {
		t.CheckpointJSON = "{}"
	}
	// Check existing (room_id, request_id) first.
	var existing domain.Turn
	var status string
	var completedAt sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT id, room_id, request_id, input_hash, status, checkpoint_json, error_code, error_message, created_at, updated_at, completed_at
		 FROM turns WHERE room_id = ? AND request_id = ?`, t.RoomID, t.RequestID,
	).Scan(&existing.ID, &existing.RoomID, &existing.RequestID, &existing.InputHash, &status, &existing.CheckpointJSON, &existing.ErrorCode, &existing.ErrorMessage, &existing.CreatedAt, &existing.UpdatedAt, &completedAt)
	if err == nil {
		existing.Status = domain.TurnStatus(status)
		if completedAt.Valid {
			v := completedAt.Int64
			existing.CompletedAt = &v
		}
		if existing.InputHash != t.InputHash {
			return domain.Turn{}, false, storage.ErrConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Turn{}, false, err
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO turns (id, room_id, request_id, input_hash, status, checkpoint_json, error_code, error_message, created_at, updated_at, completed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.RoomID, t.RequestID, t.InputHash, string(t.Status), t.CheckpointJSON, t.ErrorCode, t.ErrorMessage, t.CreatedAt, t.UpdatedAt, t.CompletedAt,
	)
	if err != nil {
		return domain.Turn{}, false, mapSQLiteErr(err)
	}
	return t, true, nil
}

func (r *turnRepo) Get(ctx context.Context, id string) (domain.Turn, error) {
	var t domain.Turn
	var status string
	var completedAt sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT id, room_id, request_id, input_hash, status, checkpoint_json, error_code, error_message, created_at, updated_at, completed_at
		 FROM turns WHERE id = ?`, id,
	).Scan(&t.ID, &t.RoomID, &t.RequestID, &t.InputHash, &status, &t.CheckpointJSON, &t.ErrorCode, &t.ErrorMessage, &t.CreatedAt, &t.UpdatedAt, &completedAt)
	if err != nil {
		return domain.Turn{}, mapSQLiteErr(err)
	}
	t.Status = domain.TurnStatus(status)
	if completedAt.Valid {
		v := completedAt.Int64
		t.CompletedAt = &v
	}
	return t, nil
}

func (r *turnRepo) Update(ctx context.Context, t domain.Turn) error {
	t.UpdatedAt = domain.NowMS()
	if t.CheckpointJSON == "" {
		t.CheckpointJSON = "{}"
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE turns SET status = ?, checkpoint_json = ?, error_code = ?, error_message = ?, updated_at = ?, completed_at = ?
		 WHERE id = ?`,
		string(t.Status), t.CheckpointJSON, t.ErrorCode, t.ErrorMessage, t.UpdatedAt, t.CompletedAt, t.ID,
	)
	if err != nil {
		return mapSQLiteErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r *turnRepo) ListByRoom(ctx context.Context, roomID string, limit int) ([]domain.Turn, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, room_id, request_id, input_hash, status, checkpoint_json, error_code, error_message, created_at, updated_at, completed_at
		 FROM turns WHERE room_id = ? ORDER BY created_at DESC LIMIT ?`, roomID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Turn
	for rows.Next() {
		var t domain.Turn
		var status string
		var completedAt sql.NullInt64
		if err := rows.Scan(&t.ID, &t.RoomID, &t.RequestID, &t.InputHash, &status, &t.CheckpointJSON, &t.ErrorCode, &t.ErrorMessage, &t.CreatedAt, &t.UpdatedAt, &completedAt); err != nil {
			return nil, err
		}
		t.Status = domain.TurnStatus(status)
		if completedAt.Valid {
			v := completedAt.Int64
			t.CompletedAt = &v
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- Messages ---

type messageRepo struct{ db dbExec }

func (r *messageRepo) Append(ctx context.Context, m domain.Message) (domain.Message, error) {
	if m.ID == "" {
		m.ID = domain.NewID()
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = domain.NowMS()
	}
	if m.Seq == 0 {
		if err := r.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM messages WHERE room_id = ?`, m.RoomID).Scan(&m.Seq); err != nil {
			return domain.Message{}, err
		}
	}
	obs, err := marshalJSONString(m.ObservedBy, "[]")
	if err != nil {
		return domain.Message{}, err
	}
	meta := m.MetadataJSON
	if meta == "" {
		meta = "{}"
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO messages (id, room_id, turn_id, seq, role, author_id, author_name, content, subtext, observed_by_json, location_tag, metadata_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.RoomID, m.TurnID, m.Seq, string(m.Role), m.AuthorID, m.AuthorName, m.Content, m.Subtext, obs, m.LocationTag, meta, m.CreatedAt,
	)
	if err != nil {
		return domain.Message{}, mapSQLiteErr(err)
	}
	return m, nil
}

func scanMessage(scan func(dest ...any) error) (domain.Message, error) {
	var m domain.Message
	var role, obs string
	if err := scan(&m.ID, &m.RoomID, &m.TurnID, &m.Seq, &role, &m.AuthorID, &m.AuthorName, &m.Content, &m.Subtext, &obs, &m.LocationTag, &m.MetadataJSON, &m.CreatedAt); err != nil {
		return domain.Message{}, mapSQLiteErr(err)
	}
	m.Role = domain.MessageRole(role)
	_ = json.Unmarshal([]byte(obs), &m.ObservedBy)
	return m, nil
}

func (r *messageRepo) Get(ctx context.Context, id string) (domain.Message, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, room_id, turn_id, seq, role, author_id, author_name, content, subtext, observed_by_json, location_tag, metadata_json, created_at
		 FROM messages WHERE id = ?`, id,
	)
	return scanMessage(row.Scan)
}

func (r *messageRepo) List(ctx context.Context, roomID string, beforeSeq int64, limit int) ([]domain.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if beforeSeq > 0 {
		rows, err = r.db.QueryContext(ctx,
			`SELECT id, room_id, turn_id, seq, role, author_id, author_name, content, subtext, observed_by_json, location_tag, metadata_json, created_at
			 FROM messages WHERE room_id = ? AND seq < ? ORDER BY seq DESC LIMIT ?`, roomID, beforeSeq, limit,
		)
	} else {
		rows, err = r.db.QueryContext(ctx,
			`SELECT id, room_id, turn_id, seq, role, author_id, author_name, content, subtext, observed_by_json, location_tag, metadata_json, created_at
			 FROM messages WHERE room_id = ? ORDER BY seq DESC LIMIT ?`, roomID, limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Message
	for rows.Next() {
		m, err := scanMessage(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Reverse(out)
	return out, nil
}

// --- Tool Invocations & User Questions ---

type toolRepo struct{ db dbExec }

func (r *toolRepo) CreateInvocation(ctx context.Context, inv domain.ToolInvocation) error {
	if inv.ID == "" {
		inv.ID = domain.NewID()
	}
	now := domain.NowMS()
	if inv.CreatedAt == 0 {
		inv.CreatedAt = now
	}
	if inv.UpdatedAt == 0 {
		inv.UpdatedAt = inv.CreatedAt
	}
	if inv.ArgsJSON == "" {
		inv.ArgsJSON = "{}"
	}
	var artID any
	if inv.ArtifactID != "" {
		artID = inv.ArtifactID
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO tool_invocations (id, turn_id, room_id, actor_id, call_id, tool_name, tool_source, args_json, gate_mode, decision_reason, status, result_summary, artifact_id, error_message, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		inv.ID, inv.TurnID, inv.RoomID, inv.ActorID, inv.CallID, inv.ToolName, string(inv.ToolSource), inv.ArgsJSON, string(inv.GateMode), inv.DecisionReason, string(inv.Status), inv.ResultSummary, artID, inv.ErrorMessage, inv.CreatedAt, inv.UpdatedAt,
	)
	return mapSQLiteErr(err)
}

func scanInvocation(scan func(dest ...any) error) (domain.ToolInvocation, error) {
	var inv domain.ToolInvocation
	var source, gate, status string
	var artID sql.NullString
	if err := scan(&inv.ID, &inv.TurnID, &inv.RoomID, &inv.ActorID, &inv.CallID, &inv.ToolName, &source, &inv.ArgsJSON, &gate, &inv.DecisionReason, &status, &inv.ResultSummary, &artID, &inv.ErrorMessage, &inv.CreatedAt, &inv.UpdatedAt); err != nil {
		return domain.ToolInvocation{}, mapSQLiteErr(err)
	}
	inv.ToolSource = domain.ToolSource(source)
	inv.GateMode = domain.ApprovalMode(gate)
	inv.Status = domain.ToolStatus(status)
	if artID.Valid {
		inv.ArtifactID = artID.String
	}
	return inv, nil
}

func (r *toolRepo) GetInvocation(ctx context.Context, id string) (domain.ToolInvocation, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, turn_id, room_id, actor_id, call_id, tool_name, tool_source, args_json, gate_mode, decision_reason, status, result_summary, artifact_id, error_message, created_at, updated_at
		 FROM tool_invocations WHERE id = ?`, id,
	)
	return scanInvocation(row.Scan)
}

func (r *toolRepo) UpdateInvocation(ctx context.Context, inv domain.ToolInvocation) error {
	inv.UpdatedAt = domain.NowMS()
	var artID any
	if inv.ArtifactID != "" {
		artID = inv.ArtifactID
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE tool_invocations SET status = ?, decision_reason = ?, result_summary = ?, artifact_id = ?, error_message = ?, updated_at = ?
		 WHERE id = ?`,
		string(inv.Status), inv.DecisionReason, inv.ResultSummary, artID, inv.ErrorMessage, inv.UpdatedAt, inv.ID,
	)
	if err != nil {
		return mapSQLiteErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r *toolRepo) ListInvocationsByTurn(ctx context.Context, turnID string) ([]domain.ToolInvocation, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, turn_id, room_id, actor_id, call_id, tool_name, tool_source, args_json, gate_mode, decision_reason, status, result_summary, artifact_id, error_message, created_at, updated_at
		 FROM tool_invocations WHERE turn_id = ? ORDER BY created_at ASC, id ASC`, turnID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ToolInvocation
	for rows.Next() {
		inv, err := scanInvocation(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (r *toolRepo) CreateQuestion(ctx context.Context, q domain.UserQuestion) error {
	if q.ID == "" {
		q.ID = domain.NewID()
	}
	if q.CreatedAt == 0 {
		q.CreatedAt = domain.NowMS()
	}
	choices, err := marshalJSONString(q.Choices, "[]")
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO user_questions (id, turn_id, room_id, actor_id, prompt, choices_json, allow_freeform, status, answer_text, created_at, answered_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		q.ID, q.TurnID, q.RoomID, q.ActorID, q.Prompt, choices, boolToInt(q.AllowFreeform), string(q.Status), q.AnswerText, q.CreatedAt, q.AnsweredAt,
	)
	return mapSQLiteErr(err)
}

func scanQuestion(scan func(dest ...any) error) (domain.UserQuestion, error) {
	var q domain.UserQuestion
	var choices, status string
	var allowFree int
	var answeredAt sql.NullInt64
	if err := scan(&q.ID, &q.TurnID, &q.RoomID, &q.ActorID, &q.Prompt, &choices, &allowFree, &status, &q.AnswerText, &q.CreatedAt, &answeredAt); err != nil {
		return domain.UserQuestion{}, mapSQLiteErr(err)
	}
	_ = json.Unmarshal([]byte(choices), &q.Choices)
	q.AllowFreeform = intToBool(allowFree)
	q.Status = domain.QuestionStatus(status)
	if answeredAt.Valid {
		v := answeredAt.Int64
		q.AnsweredAt = &v
	}
	return q, nil
}

func (r *toolRepo) GetQuestion(ctx context.Context, id string) (domain.UserQuestion, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, turn_id, room_id, actor_id, prompt, choices_json, allow_freeform, status, answer_text, created_at, answered_at
		 FROM user_questions WHERE id = ?`, id,
	)
	return scanQuestion(row.Scan)
}

func (r *toolRepo) UpdateQuestion(ctx context.Context, q domain.UserQuestion) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE user_questions SET status = ?, answer_text = ?, answered_at = ? WHERE id = ?`,
		string(q.Status), q.AnswerText, q.AnsweredAt, q.ID,
	)
	if err != nil {
		return mapSQLiteErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r *toolRepo) ListQuestionsByTurn(ctx context.Context, turnID string) ([]domain.UserQuestion, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, turn_id, room_id, actor_id, prompt, choices_json, allow_freeform, status, answer_text, created_at, answered_at
		 FROM user_questions WHERE turn_id = ? ORDER BY created_at ASC, id ASC`, turnID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.UserQuestion
	for rows.Next() {
		q, err := scanQuestion(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// --- Memories & Value Jobs ---

type memoryRepo struct{ db dbExec }

func (r *memoryRepo) CreateMemory(ctx context.Context, m domain.Memory) error {
	if m.ID == "" {
		m.ID = domain.NewID()
	}
	if m.Revision == 0 {
		m.Revision = 1
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = domain.NowMS()
	}
	obs, err := marshalJSONString(m.ObservedBy, "[]")
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO memories (id, room_id, owner_id, source_turn_id, kind, content, observed_by_json, occurred_at, pinned, status, revision, embedding_model, embedding_dim, embedding_blob, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.RoomID, m.OwnerID, m.SourceTurnID, string(m.Kind), m.Content, obs, m.OccurredAt, boolToInt(m.Pinned), string(m.Status), m.Revision, m.EmbeddingModel, m.EmbeddingDim, encodeFloat32s(m.Embedding), m.CreatedAt,
	)
	return mapSQLiteErr(err)
}

func scanMemory(scan func(dest ...any) error) (domain.Memory, error) {
	var m domain.Memory
	var kind, obs, status string
	var pinned int
	var embBlob []byte
	if err := scan(&m.ID, &m.RoomID, &m.OwnerID, &m.SourceTurnID, &kind, &m.Content, &obs, &m.OccurredAt, &pinned, &status, &m.Revision, &m.EmbeddingModel, &m.EmbeddingDim, &embBlob, &m.CreatedAt); err != nil {
		return domain.Memory{}, mapSQLiteErr(err)
	}
	m.Kind = domain.MemoryKind(kind)
	m.Status = domain.MemoryStatus(status)
	m.Pinned = intToBool(pinned)
	_ = json.Unmarshal([]byte(obs), &m.ObservedBy)
	m.Embedding = decodeFloat32s(embBlob)
	return m, nil
}

func (r *memoryRepo) GetMemory(ctx context.Context, id string) (domain.Memory, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, room_id, owner_id, source_turn_id, kind, content, observed_by_json, occurred_at, pinned, status, revision, embedding_model, embedding_dim, embedding_blob, created_at
		 FROM memories WHERE id = ?`, id,
	)
	return scanMemory(row.Scan)
}

func (r *memoryRepo) ListMemories(ctx context.Context, roomID, ownerID, observerID string, limit int) ([]domain.Memory, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT id, room_id, owner_id, source_turn_id, kind, content, observed_by_json, occurred_at, pinned, status, revision, embedding_model, embedding_dim, embedding_blob, created_at
	          FROM memories WHERE room_id = ? AND status = 'active'`
	args := []any{roomID}
	if ownerID != "" {
		query += ` AND owner_id = ?`
		args = append(args, ownerID)
	}
	query += ` ORDER BY occurred_at DESC, created_at DESC`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Memory
	for rows.Next() {
		m, err := scanMemory(rows.Scan)
		if err != nil {
			return nil, err
		}
		if observerID != "" && !slices.Contains(m.ObservedBy, observerID) {
			continue
		}
		out = append(out, m)
		if len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

func (r *memoryRepo) UpdateMemory(ctx context.Context, m domain.Memory) error {
	obs, err := marshalJSONString(m.ObservedBy, "[]")
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE memories SET content = ?, observed_by_json = ?, pinned = ?, status = ?, revision = ?, embedding_model = ?, embedding_dim = ?, embedding_blob = ?
		 WHERE id = ?`,
		m.Content, obs, boolToInt(m.Pinned), string(m.Status), m.Revision, m.EmbeddingModel, m.EmbeddingDim, encodeFloat32s(m.Embedding), m.ID,
	)
	if err != nil {
		return mapSQLiteErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r *memoryRepo) CreateValueJob(ctx context.Context, job domain.ValueJob) error {
	if job.ID == "" {
		job.ID = domain.NewID()
	}
	now := domain.NowMS()
	if job.CreatedAt == 0 {
		job.CreatedAt = now
	}
	if job.UpdatedAt == 0 {
		job.UpdatedAt = job.CreatedAt
	}
	if job.CandidatesJSON == "" {
		job.CandidatesJSON = "[]"
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO value_jobs (id, room_id, character_id, source_turn_id, base_values_revision, candidates_json, status, attempts, last_error, next_run_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.RoomID, job.CharacterID, job.SourceTurnID, job.BaseValuesRevision, job.CandidatesJSON, string(job.Status), job.Attempts, job.LastError, job.NextRunAt, job.CreatedAt, job.UpdatedAt,
	)
	return mapSQLiteErr(err)
}

func (r *memoryRepo) ClaimNextValueJob(ctx context.Context, nowMS int64) (*domain.ValueJob, error) {
	var job domain.ValueJob
	var status string
	err := r.db.QueryRowContext(ctx,
		`SELECT id, room_id, character_id, source_turn_id, base_values_revision, candidates_json, status, attempts, last_error, next_run_at, created_at, updated_at
		 FROM value_jobs WHERE status = 'queued' AND next_run_at <= ? ORDER BY next_run_at ASC, created_at ASC LIMIT 1`, nowMS,
	).Scan(&job.ID, &job.RoomID, &job.CharacterID, &job.SourceTurnID, &job.BaseValuesRevision, &job.CandidatesJSON, &status, &job.Attempts, &job.LastError, &job.NextRunAt, &job.CreatedAt, &job.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	job.Status = domain.ValueJobRunning
	job.UpdatedAt = nowMS
	res, err := r.db.ExecContext(ctx,
		`UPDATE value_jobs SET status = 'running', updated_at = ? WHERE id = ? AND status = 'queued'`,
		nowMS, job.ID,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	return &job, nil
}

func (r *memoryRepo) UpdateValueJob(ctx context.Context, job domain.ValueJob) error {
	job.UpdatedAt = domain.NowMS()
	res, err := r.db.ExecContext(ctx,
		`UPDATE value_jobs SET status = ?, attempts = ?, last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ?`,
		string(job.Status), job.Attempts, job.LastError, job.NextRunAt, job.UpdatedAt, job.ID,
	)
	if err != nil {
		return mapSQLiteErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

// --- Knowledge ---

type knowledgeRepo struct{ db dbExec }

func (r *knowledgeRepo) CreateDoc(ctx context.Context, doc domain.KnowledgeDoc) (domain.KnowledgeDoc, error) {
	if doc.ID == "" {
		doc.ID = domain.NewID()
	}
	if doc.Revision == 0 {
		doc.Revision = 1
	}
	if doc.Status == "" {
		doc.Status = domain.KnowledgeStatusActive
	}
	if doc.ContentSHA256 == "" {
		doc.ContentSHA256 = domain.HashInput(doc.Content)
	}
	now := domain.NowMS()
	if doc.CreatedAt == 0 {
		doc.CreatedAt = now
	}
	if doc.UpdatedAt == 0 {
		doc.UpdatedAt = doc.CreatedAt
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO knowledge_docs (id, scope, title, source_uri, media_type, content, content_sha256, revision, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		doc.ID, doc.Scope, doc.Title, doc.SourceURI, doc.MediaType, doc.Content, doc.ContentSHA256, doc.Revision, string(doc.Status), doc.CreatedAt, doc.UpdatedAt,
	)
	if err != nil {
		return domain.KnowledgeDoc{}, mapSQLiteErr(err)
	}
	return doc, nil
}

func (r *knowledgeRepo) GetDoc(ctx context.Context, id string) (domain.KnowledgeDoc, error) {
	var d domain.KnowledgeDoc
	var status string
	err := r.db.QueryRowContext(ctx,
		`SELECT id, scope, title, source_uri, media_type, content, content_sha256, revision, status, created_at, updated_at
		 FROM knowledge_docs WHERE id = ? AND status = 'active'`, id,
	).Scan(&d.ID, &d.Scope, &d.Title, &d.SourceURI, &d.MediaType, &d.Content, &d.ContentSHA256, &d.Revision, &status, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return domain.KnowledgeDoc{}, mapSQLiteErr(err)
	}
	d.Status = domain.KnowledgeStatus(status)
	return d, nil
}

func (r *knowledgeRepo) ListDocs(ctx context.Context, scope string) ([]domain.KnowledgeDoc, error) {
	query := `SELECT id, scope, title, source_uri, media_type, content, content_sha256, revision, status, created_at, updated_at
	          FROM knowledge_docs WHERE status = 'active'`
	var args []any
	if scope != "" {
		query += ` AND scope = ?`
		args = append(args, scope)
	}
	query += ` ORDER BY updated_at DESC, id ASC`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.KnowledgeDoc
	for rows.Next() {
		var d domain.KnowledgeDoc
		var status string
		if err := rows.Scan(&d.ID, &d.Scope, &d.Title, &d.SourceURI, &d.MediaType, &d.Content, &d.ContentSHA256, &d.Revision, &status, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		d.Status = domain.KnowledgeStatus(status)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *knowledgeRepo) UpdateDoc(ctx context.Context, doc domain.KnowledgeDoc) (domain.KnowledgeDoc, error) {
	expected := doc.Revision
	doc.Revision++
	doc.UpdatedAt = domain.NowMS()
	doc.ContentSHA256 = domain.HashInput(doc.Content)
	res, err := r.db.ExecContext(ctx,
		`UPDATE knowledge_docs SET scope = ?, title = ?, source_uri = ?, media_type = ?, content = ?, content_sha256 = ?, revision = ?, updated_at = ?
		 WHERE id = ? AND revision = ? AND status = 'active'`,
		doc.Scope, doc.Title, doc.SourceURI, doc.MediaType, doc.Content, doc.ContentSHA256, doc.Revision, doc.UpdatedAt, doc.ID, expected,
	)
	if err != nil {
		return domain.KnowledgeDoc{}, mapSQLiteErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := r.GetDoc(ctx, doc.ID); errors.Is(err, storage.ErrNotFound) {
			return domain.KnowledgeDoc{}, storage.ErrNotFound
		}
		return domain.KnowledgeDoc{}, storage.ErrConflict
	}
	return doc, nil
}

func (r *knowledgeRepo) DeleteDoc(ctx context.Context, id string, updatedAt int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE knowledge_docs SET status = 'deleted', updated_at = ? WHERE id = ? AND status = 'active'`,
		updatedAt, id,
	)
	return err
}

func (r *knowledgeRepo) ReplaceChunks(ctx context.Context, docID string, chunks []domain.KnowledgeChunk) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM knowledge_chunks WHERE doc_id = ?`, docID); err != nil {
		return err
	}
	for _, ch := range chunks {
		if ch.ID == "" {
			ch.ID = domain.NewID()
		}
		ch.DocID = docID
		_, err := r.db.ExecContext(ctx,
			`INSERT INTO knowledge_chunks (id, doc_id, doc_revision, chunk_ordinal, char_offset, text, embedding_model, embedding_dim, embedding_blob)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ch.ID, ch.DocID, ch.DocRevision, ch.ChunkOrdinal, ch.CharOffset, ch.Text, ch.EmbeddingModel, ch.EmbeddingDim, encodeFloat32s(ch.Embedding),
		)
		if err != nil {
			return mapSQLiteErr(err)
		}
	}
	return nil
}

func (r *knowledgeRepo) ListChunksByDoc(ctx context.Context, docID string) ([]domain.KnowledgeChunk, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, doc_id, doc_revision, chunk_ordinal, char_offset, text, embedding_model, embedding_dim, embedding_blob
		 FROM knowledge_chunks WHERE doc_id = ? ORDER BY chunk_ordinal ASC`, docID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.KnowledgeChunk
	for rows.Next() {
		var ch domain.KnowledgeChunk
		var emb []byte
		if err := rows.Scan(&ch.ID, &ch.DocID, &ch.DocRevision, &ch.ChunkOrdinal, &ch.CharOffset, &ch.Text, &ch.EmbeddingModel, &ch.EmbeddingDim, &emb); err != nil {
			return nil, err
		}
		ch.Embedding = decodeFloat32s(emb)
		out = append(out, ch)
	}
	return out, rows.Err()
}

func (r *knowledgeRepo) GetChunk(ctx context.Context, id string) (domain.KnowledgeChunk, error) {
	var ch domain.KnowledgeChunk
	var emb []byte
	err := r.db.QueryRowContext(ctx,
		`SELECT id, doc_id, doc_revision, chunk_ordinal, char_offset, text, embedding_model, embedding_dim, embedding_blob
		 FROM knowledge_chunks WHERE id = ?`, id,
	).Scan(&ch.ID, &ch.DocID, &ch.DocRevision, &ch.ChunkOrdinal, &ch.CharOffset, &ch.Text, &ch.EmbeddingModel, &ch.EmbeddingDim, &emb)
	if err != nil {
		return domain.KnowledgeChunk{}, mapSQLiteErr(err)
	}
	ch.Embedding = decodeFloat32s(emb)
	return ch, nil
}

// --- Outbox ---

type outboxRepo struct{ db dbExec }

func (r *outboxRepo) Enqueue(ctx context.Context, entry domain.OutboxEntry) (int64, error) {
	if entry.Status == "" {
		entry.Status = domain.OutboxStatusPending
	}
	if entry.CreatedAt == 0 {
		entry.CreatedAt = domain.NowMS()
	}
	if entry.NextAttemptAt == 0 {
		entry.NextAttemptAt = entry.CreatedAt
	}
	if entry.PayloadJSON == "" {
		entry.PayloadJSON = "{}"
	}
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO index_outbox (entity_type, entity_id, op, revision, payload_json, status, attempts, next_attempt_at, last_error, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.EntityType, entry.EntityID, string(entry.Op), entry.Revision, entry.PayloadJSON, string(entry.Status), entry.Attempts, entry.NextAttemptAt, entry.LastError, entry.CreatedAt,
	)
	if err != nil {
		return 0, mapSQLiteErr(err)
	}
	return res.LastInsertId()
}

func (r *outboxRepo) ListPending(ctx context.Context, nowMS int64, limit int) ([]domain.OutboxEntry, error) {
	if limit <= 0 {
		limit = 32
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT seq, entity_type, entity_id, op, revision, payload_json, status, attempts, next_attempt_at, last_error, created_at
		 FROM index_outbox
		 WHERE status IN ('pending', 'embedding_retry') AND next_attempt_at <= ?
		 ORDER BY seq ASC LIMIT ?`, nowMS, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.OutboxEntry
	for rows.Next() {
		var e domain.OutboxEntry
		var op, status string
		if err := rows.Scan(&e.Seq, &e.EntityType, &e.EntityID, &op, &e.Revision, &e.PayloadJSON, &status, &e.Attempts, &e.NextAttemptAt, &e.LastError, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Op = domain.OutboxOp(op)
		e.Status = domain.OutboxStatus(status)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *outboxRepo) MarkDone(ctx context.Context, seq int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE index_outbox SET status = 'done', last_error = '' WHERE seq = ?`, seq)
	return err
}

func (r *outboxRepo) MarkRetry(ctx context.Context, seq int64, status domain.OutboxStatus, nextAttemptAt int64, lastErr string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE index_outbox SET status = ?, attempts = attempts + 1, next_attempt_at = ?, last_error = ? WHERE seq = ?`,
		string(status), nextAttemptAt, lastErr, seq,
	)
	return err
}

// --- Artifacts ---

type artifactRepo struct{ db dbExec }

func (r *artifactRepo) Create(ctx context.Context, a domain.Artifact) error {
	now := domain.NowMS()
	if a.CreatedAt == 0 {
		a.CreatedAt = now
	}
	if a.LastAccessedAt == 0 {
		a.LastAccessedAt = a.CreatedAt
	}
	if a.SourceMetaJSON == "" {
		a.SourceMetaJSON = "{}"
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO artifacts (id, kind, media_type, byte_size, sha256, blob_key, source_meta_json, pinned, created_at, last_accessed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, string(a.Kind), a.MediaType, a.ByteSize, a.SHA256, a.BlobKey, a.SourceMetaJSON, boolToInt(a.Pinned), a.CreatedAt, a.LastAccessedAt,
	)
	return mapSQLiteErr(err)
}

func (r *artifactRepo) Get(ctx context.Context, id string) (domain.Artifact, error) {
	var a domain.Artifact
	var kind string
	var pinned int
	err := r.db.QueryRowContext(ctx,
		`SELECT id, kind, media_type, byte_size, sha256, blob_key, source_meta_json, pinned, created_at, last_accessed_at
		 FROM artifacts WHERE id = ?`, id,
	).Scan(&a.ID, &kind, &a.MediaType, &a.ByteSize, &a.SHA256, &a.BlobKey, &a.SourceMetaJSON, &pinned, &a.CreatedAt, &a.LastAccessedAt)
	if err != nil {
		return domain.Artifact{}, mapSQLiteErr(err)
	}
	a.Kind = domain.ArtifactKind(kind)
	a.Pinned = intToBool(pinned)
	return a, nil
}

func (r *artifactRepo) Touch(ctx context.Context, id string, accessedAt int64) error {
	res, err := r.db.ExecContext(ctx, `UPDATE artifacts SET last_accessed_at = ? WHERE id = ?`, accessedAt, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r *artifactRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM artifacts WHERE id = ?`, id)
	return err
}
