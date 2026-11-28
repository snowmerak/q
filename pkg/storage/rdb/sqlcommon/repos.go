package sqlcommon

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

func marshalJSONString(v any, fallback string) (string, error) {
	if v == nil {
		return fallback, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if string(b) == "null" {
		return fallback, nil
	}
	return string(b), nil
}

func encodeEmbedding(vec []float32) (dim int, blob []byte) {
	if len(vec) == 0 {
		return 0, nil
	}
	buf := make([]byte, len(vec)*4)
	for i, f := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return len(vec), buf
}

func decodeEmbedding(dim int, blob []byte) []float32 {
	if dim <= 0 || len(blob) != dim*4 {
		return nil
	}
	out := make([]float32, dim)
	for i := 0; i < dim; i++ {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return out
}

// --- Personas ---

type personaRepo struct{ repoBase }

func (r personaRepo) Create(ctx context.Context, p domain.Persona) (domain.Persona, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return domain.Persona{}, fmt.Errorf("%w: persona name is required", storage.ErrInvalidInput)
	}
	if p.ID == "" {
		p.ID = domain.NewID()
	}
	if p.Version <= 0 {
		p.Version = 1
	}
	now := domain.NowMS()
	if p.CreatedAt == 0 {
		p.CreatedAt = now
	}
	if p.UpdatedAt == 0 {
		p.UpdatedAt = p.CreatedAt
	}
	q := r.d.Rebind(`INSERT INTO personas (id, name, description, avatar_artifact_id, is_default, version, created_at, updated_at, deleted_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)`)
	if _, err := r.q.ExecContext(ctx, q, p.ID, p.Name, p.Description, p.AvatarArtifactID, boolToInt(p.IsDefault), p.Version, p.CreatedAt, p.UpdatedAt); err != nil {
		return domain.Persona{}, r.mapErr(err)
	}
	return p, nil
}

func scanPersona(scan func(dest ...any) error) (domain.Persona, error) {
	var p domain.Persona
	var isDef int
	var deleted sql.NullInt64
	if err := scan(&p.ID, &p.Name, &p.Description, &p.AvatarArtifactID, &isDef, &p.Version, &p.CreatedAt, &p.UpdatedAt, &deleted); err != nil {
		return domain.Persona{}, err
	}
	p.IsDefault = isDef != 0
	if deleted.Valid {
		v := deleted.Int64
		p.DeletedAt = &v
	}
	return p, nil
}

func (r personaRepo) Get(ctx context.Context, id string) (domain.Persona, error) {
	q := r.d.Rebind(`SELECT id, name, description, avatar_artifact_id, is_default, version, created_at, updated_at, deleted_at FROM personas WHERE id = ? AND deleted_at IS NULL`)
	p, err := scanPersona(r.q.QueryRowContext(ctx, q, id).Scan)
	if err != nil {
		return domain.Persona{}, r.mapErr(err)
	}
	return p, nil
}

func (r personaRepo) List(ctx context.Context) ([]domain.Persona, error) {
	rows, err := r.q.QueryContext(ctx, r.d.Rebind(`SELECT id, name, description, avatar_artifact_id, is_default, version, created_at, updated_at, deleted_at FROM personas WHERE deleted_at IS NULL ORDER BY created_at ASC`))
	if err != nil {
		return nil, r.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Persona
	for rows.Next() {
		p, err := scanPersona(rows.Scan)
		if err != nil {
			return nil, r.mapErr(err)
		}
		out = append(out, p)
	}
	return out, r.mapErr(rows.Err())
}

func (r personaRepo) Update(ctx context.Context, p domain.Persona) (domain.Persona, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.ID == "" || p.Name == "" {
		return domain.Persona{}, fmt.Errorf("%w: id and name are required", storage.ErrInvalidInput)
	}
	p.UpdatedAt = domain.NowMS()
	q := r.d.Rebind(`UPDATE personas SET name = ?, description = ?, avatar_artifact_id = ?, is_default = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ? AND deleted_at IS NULL`)
	res, err := r.q.ExecContext(ctx, q, p.Name, p.Description, p.AvatarArtifactID, boolToInt(p.IsDefault), p.UpdatedAt, p.ID, p.Version)
	if err != nil {
		return domain.Persona{}, r.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists int
		if err := r.q.QueryRowContext(ctx, r.d.Rebind(`SELECT 1 FROM personas WHERE id = ? AND deleted_at IS NULL`), p.ID).Scan(&exists); err == nil {
			return domain.Persona{}, storage.ErrConflict
		}
		return domain.Persona{}, storage.ErrNotFound
	}
	return r.Get(ctx, p.ID)
}

func (r personaRepo) Delete(ctx context.Context, id string, deletedAt int64) error {
	if deletedAt == 0 {
		deletedAt = domain.NowMS()
	}
	q := r.d.Rebind(`UPDATE personas SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`)
	res, err := r.q.ExecContext(ctx, q, deletedAt, deletedAt, id)
	if err != nil {
		return r.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

// --- Characters ---

type characterRepo struct{ repoBase }

func (r characterRepo) Create(ctx context.Context, c domain.Character) (domain.Character, error) {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return domain.Character{}, fmt.Errorf("%w: character name is required", storage.ErrInvalidInput)
	}
	if c.ID == "" {
		c.ID = domain.NewID()
	}
	if c.Version <= 0 {
		c.Version = 1
	}
	now := domain.NowMS()
	if c.CreatedAt == 0 {
		c.CreatedAt = now
	}
	if c.UpdatedAt == 0 {
		c.UpdatedAt = c.CreatedAt
	}
	valsJSON, err := marshalJSONString(c.InitialValues, `[]`)
	if err != nil {
		return domain.Character{}, fmt.Errorf("%w: invalid initial_values: %v", storage.ErrInvalidInput, err)
	}
	initJSON, err := marshalJSONString(c.InitialState, `{}`)
	if err != nil {
		return domain.Character{}, fmt.Errorf("%w: invalid initial_state: %v", storage.ErrInvalidInput, err)
	}
	var prefsArg sql.NullString
	if c.ModelPrefs != nil {
		s, err := marshalJSONString(c.ModelPrefs, ``)
		if err != nil {
			return domain.Character{}, fmt.Errorf("%w: invalid model_prefs: %v", storage.ErrInvalidInput, err)
		}
		prefsArg = sql.NullString{String: s, Valid: true}
	}
	approvalStr := ""
	if c.ApprovalPolicyOverride != nil {
		approvalStr = string(*c.ApprovalPolicyOverride)
	}
	q := r.d.Rebind(`INSERT INTO characters (id, name, avatar_artifact_id, personality, background, appearance, initial_values_json, initial_state_json, model_prefs_json, approval_policy_override, version, created_at, updated_at, deleted_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`)
	if _, err := r.q.ExecContext(ctx, q, c.ID, c.Name, c.AvatarArtifactID, c.Personality, c.Background, c.Appearance, valsJSON, initJSON, prefsArg, approvalStr, c.Version, c.CreatedAt, c.UpdatedAt); err != nil {
		return domain.Character{}, r.mapErr(err)
	}
	return c, nil
}

func scanCharacter(scan func(dest ...any) error) (domain.Character, error) {
	var c domain.Character
	var valsJSON, initJSON, approvalStr string
	var prefsJSON sql.NullString
	var deleted sql.NullInt64
	if err := scan(&c.ID, &c.Name, &c.AvatarArtifactID, &c.Personality, &c.Background, &c.Appearance, &valsJSON, &initJSON, &prefsJSON, &approvalStr, &c.Version, &c.CreatedAt, &c.UpdatedAt, &deleted); err != nil {
		return domain.Character{}, err
	}
	if valsJSON != "" && valsJSON != "[]" {
		_ = json.Unmarshal([]byte(valsJSON), &c.InitialValues)
	}
	if initJSON != "" && initJSON != "{}" {
		var initState domain.InitialState
		if err := json.Unmarshal([]byte(initJSON), &initState); err == nil {
			c.InitialState = &initState
		}
	}
	if prefsJSON.Valid && prefsJSON.String != "" {
		var prefs domain.ModelPrefs
		if err := json.Unmarshal([]byte(prefsJSON.String), &prefs); err == nil {
			c.ModelPrefs = &prefs
		}
	}
	if approvalStr != "" {
		ap := domain.ApprovalMode(approvalStr)
		c.ApprovalPolicyOverride = &ap
	}
	if deleted.Valid {
		v := deleted.Int64
		c.DeletedAt = &v
	}
	return c, nil
}

func (r characterRepo) Get(ctx context.Context, id string) (domain.Character, error) {
	q := r.d.Rebind(`SELECT id, name, avatar_artifact_id, personality, background, appearance, initial_values_json, initial_state_json, model_prefs_json, approval_policy_override, version, created_at, updated_at, deleted_at FROM characters WHERE id = ? AND deleted_at IS NULL`)
	c, err := scanCharacter(r.q.QueryRowContext(ctx, q, id).Scan)
	if err != nil {
		return domain.Character{}, r.mapErr(err)
	}
	return c, nil
}

func (r characterRepo) List(ctx context.Context) ([]domain.Character, error) {
	rows, err := r.q.QueryContext(ctx, r.d.Rebind(`SELECT id, name, avatar_artifact_id, personality, background, appearance, initial_values_json, initial_state_json, model_prefs_json, approval_policy_override, version, created_at, updated_at, deleted_at FROM characters WHERE deleted_at IS NULL ORDER BY created_at ASC`))
	if err != nil {
		return nil, r.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Character
	for rows.Next() {
		c, err := scanCharacter(rows.Scan)
		if err != nil {
			return nil, r.mapErr(err)
		}
		out = append(out, c)
	}
	return out, r.mapErr(rows.Err())
}

func (r characterRepo) Update(ctx context.Context, c domain.Character) (domain.Character, error) {
	c.Name = strings.TrimSpace(c.Name)
	if c.ID == "" || c.Name == "" {
		return domain.Character{}, fmt.Errorf("%w: id and name are required", storage.ErrInvalidInput)
	}
	c.UpdatedAt = domain.NowMS()
	valsJSON, err := marshalJSONString(c.InitialValues, `[]`)
	if err != nil {
		return domain.Character{}, fmt.Errorf("%w: invalid initial_values: %v", storage.ErrInvalidInput, err)
	}
	initJSON, err := marshalJSONString(c.InitialState, `{}`)
	if err != nil {
		return domain.Character{}, fmt.Errorf("%w: invalid initial_state: %v", storage.ErrInvalidInput, err)
	}
	var prefsArg sql.NullString
	if c.ModelPrefs != nil {
		s, err := marshalJSONString(c.ModelPrefs, ``)
		if err != nil {
			return domain.Character{}, fmt.Errorf("%w: invalid model_prefs: %v", storage.ErrInvalidInput, err)
		}
		prefsArg = sql.NullString{String: s, Valid: true}
	}
	approvalStr := ""
	if c.ApprovalPolicyOverride != nil {
		approvalStr = string(*c.ApprovalPolicyOverride)
	}
	q := r.d.Rebind(`UPDATE characters SET name = ?, avatar_artifact_id = ?, personality = ?, background = ?, appearance = ?, initial_values_json = ?, initial_state_json = ?, model_prefs_json = ?, approval_policy_override = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ? AND deleted_at IS NULL`)
	res, err := r.q.ExecContext(ctx, q, c.Name, c.AvatarArtifactID, c.Personality, c.Background, c.Appearance, valsJSON, initJSON, prefsArg, approvalStr, c.UpdatedAt, c.ID, c.Version)
	if err != nil {
		return domain.Character{}, r.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists int
		if err := r.q.QueryRowContext(ctx, r.d.Rebind(`SELECT 1 FROM characters WHERE id = ? AND deleted_at IS NULL`), c.ID).Scan(&exists); err == nil {
			return domain.Character{}, storage.ErrConflict
		}
		return domain.Character{}, storage.ErrNotFound
	}
	return r.Get(ctx, c.ID)
}

func (r characterRepo) Delete(ctx context.Context, id string, deletedAt int64) error {
	if deletedAt == 0 {
		deletedAt = domain.NowMS()
	}
	q := r.d.Rebind(`UPDATE characters SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`)
	res, err := r.q.ExecContext(ctx, q, deletedAt, deletedAt, id)
	if err != nil {
		return r.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

// --- Rooms & Members ---

type roomRepo struct{ repoBase }

func (r roomRepo) insertMember(ctx context.Context, q Execer, m domain.RoomMember) error {
	if m.JoinedAt == 0 {
		m.JoinedAt = domain.NowMS()
	}
	if m.BaseSpeed <= 0 {
		m.BaseSpeed = 50
	}
	if m.ActorState.Relationships == nil {
		m.ActorState.Relationships = map[string]domain.Relationship{}
	}
	charJSON, err := marshalJSONString(m.CharacterSnapshot, `{}`)
	if err != nil {
		return fmt.Errorf("%w: invalid character_snapshot: %v", storage.ErrInvalidInput, err)
	}
	stateJSON, err := marshalJSONString(m.ActorState, `{}`)
	if err != nil {
		return fmt.Errorf("%w: invalid actor_state: %v", storage.ErrInvalidInput, err)
	}
	stmt := r.d.UpsertMemberSQL()
	_, err = q.ExecContext(ctx, stmt, m.RoomID, m.CharacterID, charJSON, m.BaseSpeed, stateJSON, boolToInt(m.Active), m.JoinedAt)
	return r.mapErr(err)
}

func (r roomRepo) Create(ctx context.Context, rm domain.Room, members []domain.RoomMember) (domain.Room, error) {
	rm.Title = strings.TrimSpace(rm.Title)
	if rm.Title == "" || !rm.Mode.Valid() {
		return domain.Room{}, fmt.Errorf("%w: room title and valid mode are required", storage.ErrInvalidInput)
	}
	if rm.ID == "" {
		rm.ID = domain.NewID()
	}
	if rm.Version <= 0 {
		rm.Version = 1
	}
	if rm.ContextVersion <= 0 {
		rm.ContextVersion = 1
	}
	if rm.MaxTurnsPerInput <= 0 {
		rm.MaxTurnsPerInput = 3
	}
	rm.WorkDir = strings.TrimSpace(rm.WorkDir)
	now := domain.NowMS()
	if rm.CreatedAt == 0 {
		rm.CreatedAt = now
	}
	if rm.UpdatedAt == 0 {
		rm.UpdatedAt = rm.CreatedAt
	}

	personaJSON, err := marshalJSONString(rm.PersonaSnapshot, `{}`)
	if err != nil {
		return domain.Room{}, fmt.Errorf("%w: invalid persona_snapshot: %v", storage.ErrInvalidInput, err)
	}
	var approvalArg sql.NullString
	if rm.ApprovalModeOverride != nil {
		approvalArg = sql.NullString{String: string(*rm.ApprovalModeOverride), Valid: true}
	}
	var overridesArg sql.NullString
	if rm.ModelOverrides != nil {
		s, err := marshalJSONString(rm.ModelOverrides, ``)
		if err != nil {
			return domain.Room{}, fmt.Errorf("%w: invalid model_overrides: %v", storage.ErrInvalidInput, err)
		}
		overridesArg = sql.NullString{String: s, Valid: true}
	}

	run := func(exec Execer) error {
		q := r.d.Rebind(`INSERT INTO rooms (id, title, mode, persona_id, persona_snapshot_json, approval_mode_override, model_overrides_json, max_turns_per_input, work_dir, context_version, version, created_at, updated_at, deleted_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`)
		if _, err := exec.ExecContext(ctx, q, rm.ID, rm.Title, string(rm.Mode), rm.PersonaID, personaJSON, approvalArg, overridesArg, rm.MaxTurnsPerInput, rm.WorkDir, rm.ContextVersion, rm.Version, rm.CreatedAt, rm.UpdatedAt); err != nil {
			return r.mapErr(err)
		}
		for _, m := range members {
			m.RoomID = rm.ID
			if err := r.insertMember(ctx, exec, m); err != nil {
				return err
			}
		}
		return nil
	}

	if r.tx != nil {
		if err := run(r.tx); err != nil {
			return domain.Room{}, err
		}
		return rm, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Room{}, r.mapErr(err)
	}
	if err := run(tx); err != nil {
		_ = tx.Rollback()
		return domain.Room{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Room{}, r.mapErr(err)
	}
	return rm, nil
}

func scanRoom(scan func(dest ...any) error) (domain.Room, error) {
	var rm domain.Room
	var mode string
	var personaJSON string
	var approval sql.NullString
	var overrides sql.NullString
	var deleted sql.NullInt64
	if err := scan(&rm.ID, &rm.Title, &mode, &rm.PersonaID, &personaJSON, &approval, &overrides, &rm.MaxTurnsPerInput, &rm.WorkDir, &rm.ContextVersion, &rm.Version, &rm.CreatedAt, &rm.UpdatedAt, &deleted); err != nil {
		return domain.Room{}, err
	}
	rm.Mode = domain.RoomMode(mode)
	if personaJSON != "" {
		_ = json.Unmarshal([]byte(personaJSON), &rm.PersonaSnapshot)
	}
	if approval.Valid && approval.String != "" {
		m := domain.ApprovalMode(approval.String)
		rm.ApprovalModeOverride = &m
	}
	if overrides.Valid && overrides.String != "" {
		var p domain.ModelPrefs
		if err := json.Unmarshal([]byte(overrides.String), &p); err == nil {
			rm.ModelOverrides = &p
		}
	}
	if deleted.Valid {
		v := deleted.Int64
		rm.DeletedAt = &v
	}
	return rm, nil
}

func (r roomRepo) Get(ctx context.Context, id string) (domain.Room, error) {
	q := r.d.Rebind(`SELECT id, title, mode, persona_id, persona_snapshot_json, approval_mode_override, model_overrides_json, max_turns_per_input, work_dir, context_version, version, created_at, updated_at, deleted_at FROM rooms WHERE id = ? AND deleted_at IS NULL`)
	rm, err := scanRoom(r.q.QueryRowContext(ctx, q, id).Scan)
	if err != nil {
		return domain.Room{}, r.mapErr(err)
	}
	return rm, nil
}

func (r roomRepo) List(ctx context.Context) ([]domain.Room, error) {
	rows, err := r.q.QueryContext(ctx, r.d.Rebind(`SELECT id, title, mode, persona_id, persona_snapshot_json, approval_mode_override, model_overrides_json, max_turns_per_input, work_dir, context_version, version, created_at, updated_at, deleted_at FROM rooms WHERE deleted_at IS NULL ORDER BY updated_at DESC`))
	if err != nil {
		return nil, r.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Room
	for rows.Next() {
		rm, err := scanRoom(rows.Scan)
		if err != nil {
			return nil, r.mapErr(err)
		}
		out = append(out, rm)
	}
	return out, r.mapErr(rows.Err())
}

func (r roomRepo) Update(ctx context.Context, rm domain.Room) (domain.Room, error) {
	rm.Title = strings.TrimSpace(rm.Title)
	rm.WorkDir = strings.TrimSpace(rm.WorkDir)
	if rm.ID == "" || rm.Title == "" {
		return domain.Room{}, fmt.Errorf("%w: room id and title are required", storage.ErrInvalidInput)
	}
	now := domain.NowMS()
	rm.UpdatedAt = now
	personaJSON, err := marshalJSONString(rm.PersonaSnapshot, `{}`)
	if err != nil {
		return domain.Room{}, fmt.Errorf("%w: invalid persona_snapshot: %v", storage.ErrInvalidInput, err)
	}
	var approvalArg sql.NullString
	if rm.ApprovalModeOverride != nil {
		approvalArg = sql.NullString{String: string(*rm.ApprovalModeOverride), Valid: true}
	}
	var overridesArg sql.NullString
	if rm.ModelOverrides != nil {
		s, err := marshalJSONString(rm.ModelOverrides, ``)
		if err != nil {
			return domain.Room{}, fmt.Errorf("%w: invalid model_overrides: %v", storage.ErrInvalidInput, err)
		}
		overridesArg = sql.NullString{String: s, Valid: true}
	}
	q := r.d.Rebind(`UPDATE rooms SET title = ?, persona_id = ?, persona_snapshot_json = ?, approval_mode_override = ?, model_overrides_json = ?, max_turns_per_input = ?, work_dir = ?, context_version = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ? AND deleted_at IS NULL`)
	res, err := r.q.ExecContext(ctx, q, rm.Title, rm.PersonaID, personaJSON, approvalArg, overridesArg, rm.MaxTurnsPerInput, rm.WorkDir, rm.ContextVersion, rm.UpdatedAt, rm.ID, rm.Version)
	if err != nil {
		return domain.Room{}, r.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists int
		err := r.q.QueryRowContext(ctx, r.d.Rebind(`SELECT 1 FROM rooms WHERE id = ? AND deleted_at IS NULL`), rm.ID).Scan(&exists)
		if err == nil {
			return domain.Room{}, storage.ErrConflict
		}
		return domain.Room{}, storage.ErrNotFound
	}
	return r.Get(ctx, rm.ID)
}

func (r roomRepo) Delete(ctx context.Context, id string, deletedAt int64) error {
	if deletedAt == 0 {
		deletedAt = domain.NowMS()
	}
	q := r.d.Rebind(`UPDATE rooms SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`)
	res, err := r.q.ExecContext(ctx, q, deletedAt, deletedAt, id)
	if err != nil {
		return r.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r roomRepo) ListMembers(ctx context.Context, roomID string) ([]domain.RoomMember, error) {
	rows, err := r.q.QueryContext(ctx, r.d.Rebind(`SELECT room_id, character_id, character_snapshot_json, base_speed, actor_state_json, active, joined_at FROM room_members WHERE room_id = ? ORDER BY joined_at ASC`), roomID)
	if err != nil {
		return nil, r.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.RoomMember
	for rows.Next() {
		var m domain.RoomMember
		var charJSON, stateJSON string
		var activeInt int
		if err := rows.Scan(&m.RoomID, &m.CharacterID, &charJSON, &m.BaseSpeed, &stateJSON, &activeInt, &m.JoinedAt); err != nil {
			return nil, r.mapErr(err)
		}
		_ = json.Unmarshal([]byte(charJSON), &m.CharacterSnapshot)
		_ = json.Unmarshal([]byte(stateJSON), &m.ActorState)
		if m.ActorState.Relationships == nil {
			m.ActorState.Relationships = map[string]domain.Relationship{}
		}
		m.Active = activeInt != 0
		out = append(out, m)
	}
	return out, r.mapErr(rows.Err())
}

func (r roomRepo) UpsertMember(ctx context.Context, m domain.RoomMember) error {
	if m.RoomID == "" || m.CharacterID == "" {
		return fmt.Errorf("%w: room_id and character_id are required", storage.ErrInvalidInput)
	}
	return r.insertMember(ctx, r.q, m)
}

// --- Turns ---

type turnRepo struct{ repoBase }

func scanTurn(scan func(dest ...any) error) (domain.Turn, error) {
	var t domain.Turn
	var status string
	var completed sql.NullInt64
	if err := scan(&t.ID, &t.RoomID, &t.RequestID, &t.InputHash, &status, &t.ErrorCode, &t.ErrorMessage, &t.CheckpointJSON, &t.CreatedAt, &t.UpdatedAt, &completed); err != nil {
		return domain.Turn{}, err
	}
	t.Status = domain.TurnStatus(status)
	if completed.Valid {
		v := completed.Int64
		t.CompletedAt = &v
	}
	return t, nil
}

func (r turnRepo) getByRoomRequest(ctx context.Context, roomID, requestID string) (domain.Turn, error) {
	q := r.d.Rebind(`SELECT id, room_id, request_id, input_hash, status, error_code, error_message, checkpoint_json, created_at, updated_at, completed_at FROM turns WHERE room_id = ? AND request_id = ?`)
	return scanTurn(r.q.QueryRowContext(ctx, q, roomID, requestID).Scan)
}

func (r turnRepo) CreateOrGetIdempotent(ctx context.Context, t domain.Turn) (domain.Turn, bool, error) {
	if t.RoomID == "" || t.RequestID == "" {
		return domain.Turn{}, false, fmt.Errorf("%w: room_id and request_id are required", storage.ErrInvalidInput)
	}
	if existing, err := r.getByRoomRequest(ctx, t.RoomID, t.RequestID); err == nil {
		if existing.InputHash != t.InputHash {
			return domain.Turn{}, false, storage.ErrConflict
		}
		return existing, false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.Turn{}, false, r.mapErr(err)
	}

	if t.ID == "" {
		t.ID = domain.NewID()
	}
	if t.Status == "" {
		t.Status = domain.TurnStatusRunning
	}
	if t.CheckpointJSON == "" {
		t.CheckpointJSON = "{}"
	}
	if t.CreatedAt == 0 {
		t.CreatedAt = domain.NowMS()
	}
	if t.UpdatedAt == 0 {
		t.UpdatedAt = t.CreatedAt
	}

	var completed sql.NullInt64
	if t.CompletedAt != nil {
		completed = sql.NullInt64{Int64: *t.CompletedAt, Valid: true}
	}
	q := r.d.Rebind(`INSERT INTO turns (id, room_id, request_id, input_hash, status, error_code, error_message, checkpoint_json, created_at, updated_at, completed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if _, err := r.q.ExecContext(ctx, q, t.ID, t.RoomID, t.RequestID, t.InputHash, string(t.Status), t.ErrorCode, t.ErrorMessage, t.CheckpointJSON, t.CreatedAt, t.UpdatedAt, completed); err != nil {
		if r.d.IsUniqueViolation(err) && r.tx == nil {
			existing, getErr := r.getByRoomRequest(ctx, t.RoomID, t.RequestID)
			if getErr == nil {
				if existing.InputHash != t.InputHash {
					return domain.Turn{}, false, storage.ErrConflict
				}
				return existing, false, nil
			}
		}
		return domain.Turn{}, false, r.mapErr(err)
	}
	return t, true, nil
}

func (r turnRepo) Get(ctx context.Context, id string) (domain.Turn, error) {
	q := r.d.Rebind(`SELECT id, room_id, request_id, input_hash, status, error_code, error_message, checkpoint_json, created_at, updated_at, completed_at FROM turns WHERE id = ?`)
	t, err := scanTurn(r.q.QueryRowContext(ctx, q, id).Scan)
	if err != nil {
		return domain.Turn{}, r.mapErr(err)
	}
	return t, nil
}

func (r turnRepo) Update(ctx context.Context, t domain.Turn) error {
	if t.CheckpointJSON == "" {
		t.CheckpointJSON = "{}"
	}
	t.UpdatedAt = domain.NowMS()
	var completed sql.NullInt64
	if t.CompletedAt != nil {
		completed = sql.NullInt64{Int64: *t.CompletedAt, Valid: true}
	}
	q := r.d.Rebind(`UPDATE turns SET status = ?, error_code = ?, error_message = ?, checkpoint_json = ?, updated_at = ?, completed_at = ? WHERE id = ?`)
	res, err := r.q.ExecContext(ctx, q, string(t.Status), t.ErrorCode, t.ErrorMessage, t.CheckpointJSON, t.UpdatedAt, completed, t.ID)
	if err != nil {
		return r.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r turnRepo) ListByRoom(ctx context.Context, roomID string, limit int) ([]domain.Turn, error) {
	if limit <= 0 {
		limit = 50
	}
	q := r.d.Rebind(`SELECT id, room_id, request_id, input_hash, status, error_code, error_message, checkpoint_json, created_at, updated_at, completed_at FROM turns WHERE room_id = ? ORDER BY created_at DESC LIMIT ?`)
	rows, err := r.q.QueryContext(ctx, q, roomID, limit)
	if err != nil {
		return nil, r.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Turn
	for rows.Next() {
		t, err := scanTurn(rows.Scan)
		if err != nil {
			return nil, r.mapErr(err)
		}
		out = append(out, t)
	}
	return out, r.mapErr(rows.Err())
}

// --- Messages ---

type messageRepo struct{ repoBase }

func scanMessage(scan func(dest ...any) error) (domain.Message, error) {
	var m domain.Message
	var role, obsJSON string
	if err := scan(&m.ID, &m.RoomID, &m.TurnID, &m.Seq, &role, &m.AuthorID, &m.AuthorName, &m.Content, &m.Subtext, &obsJSON, &m.LocationTag, &m.MetadataJSON, &m.CreatedAt); err != nil {
		return domain.Message{}, err
	}
	m.Role = domain.MessageRole(role)
	_ = json.Unmarshal([]byte(obsJSON), &m.ObservedBy)
	return m, nil
}

func (r messageRepo) Append(ctx context.Context, m domain.Message) (domain.Message, error) {
	if m.RoomID == "" || strings.TrimSpace(m.Content) == "" {
		return domain.Message{}, fmt.Errorf("%w: room_id and content are required", storage.ErrInvalidInput)
	}
	if m.ID == "" {
		m.ID = domain.NewID()
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = domain.NowMS()
	}
	if m.MetadataJSON == "" {
		m.MetadataJSON = "{}"
	}
	obsJSON, err := marshalJSONString(m.ObservedBy, `[]`)
	if err != nil {
		return domain.Message{}, fmt.Errorf("%w: invalid observed_by: %v", storage.ErrInvalidInput, err)
	}

	run := func(exec Execer) error {
		if err := r.d.BeforeAppendMessage(ctx, exec, m.RoomID); err != nil {
			return r.mapErr(err)
		}
		var nextSeq int64
		if err := exec.QueryRowContext(ctx, r.d.Rebind(`SELECT COALESCE(MAX(seq), 0) + 1 FROM messages WHERE room_id = ?`), m.RoomID).Scan(&nextSeq); err != nil {
			return r.mapErr(err)
		}
		m.Seq = nextSeq
		q := r.d.Rebind(`INSERT INTO messages (id, room_id, turn_id, seq, role, author_id, author_name, content, subtext, observed_by_json, location_tag, metadata_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		_, err := exec.ExecContext(ctx, q, m.ID, m.RoomID, m.TurnID, m.Seq, string(m.Role), m.AuthorID, m.AuthorName, m.Content, m.Subtext, obsJSON, m.LocationTag, m.MetadataJSON, m.CreatedAt)
		return r.mapErr(err)
	}

	if r.tx != nil {
		if err := run(r.tx); err != nil {
			return domain.Message{}, err
		}
		return m, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Message{}, r.mapErr(err)
	}
	if err := run(tx); err != nil {
		_ = tx.Rollback()
		return domain.Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Message{}, r.mapErr(err)
	}
	return m, nil
}

func (r messageRepo) Get(ctx context.Context, id string) (domain.Message, error) {
	q := r.d.Rebind(`SELECT id, room_id, turn_id, seq, role, author_id, author_name, content, subtext, observed_by_json, location_tag, metadata_json, created_at FROM messages WHERE id = ?`)
	m, err := scanMessage(r.q.QueryRowContext(ctx, q, id).Scan)
	if err != nil {
		return domain.Message{}, r.mapErr(err)
	}
	return m, nil
}

func (r messageRepo) List(ctx context.Context, roomID string, beforeSeq int64, limit int) ([]domain.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if beforeSeq > 0 {
		q := r.d.Rebind(`SELECT id, room_id, turn_id, seq, role, author_id, author_name, content, subtext, observed_by_json, location_tag, metadata_json, created_at FROM messages WHERE room_id = ? AND seq < ? ORDER BY seq DESC LIMIT ?`)
		rows, err = r.q.QueryContext(ctx, q, roomID, beforeSeq, limit)
	} else {
		q := r.d.Rebind(`SELECT id, room_id, turn_id, seq, role, author_id, author_name, content, subtext, observed_by_json, location_tag, metadata_json, created_at FROM messages WHERE room_id = ? ORDER BY seq DESC LIMIT ?`)
		rows, err = r.q.QueryContext(ctx, q, roomID, limit)
	}
	if err != nil {
		return nil, r.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Message
	for rows.Next() {
		m, err := scanMessage(rows.Scan)
		if err != nil {
			return nil, r.mapErr(err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, r.mapErr(err)
	}
	// Reverse to ascending seq order.
	for l, h := 0, len(out)-1; l < h; l, h = l+1, h-1 {
		out[l], out[h] = out[h], out[l]
	}
	return out, nil
}

// --- Tool Invocations & User Questions ---

type toolRepo struct{ repoBase }

func (r toolRepo) CreateInvocation(ctx context.Context, inv domain.ToolInvocation) error {
	if inv.ID == "" {
		inv.ID = domain.NewID()
	}
	if inv.ArgsJSON == "" {
		inv.ArgsJSON = "{}"
	}
	now := domain.NowMS()
	if inv.CreatedAt == 0 {
		inv.CreatedAt = now
	}
	if inv.UpdatedAt == 0 {
		inv.UpdatedAt = inv.CreatedAt
	}
	var artifactArg sql.NullString
	if inv.ArtifactID != "" {
		artifactArg = sql.NullString{String: inv.ArtifactID, Valid: true}
	}
	q := r.d.Rebind(`INSERT INTO tool_invocations (id, turn_id, room_id, actor_id, call_id, tool_name, tool_source, args_json, gate_mode, decision_reason, status, result_summary, artifact_id, error_message, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err := r.q.ExecContext(ctx, q, inv.ID, inv.TurnID, inv.RoomID, inv.ActorID, inv.CallID, inv.ToolName, string(inv.ToolSource), inv.ArgsJSON, string(inv.GateMode), inv.DecisionReason, string(inv.Status), inv.ResultSummary, artifactArg, inv.ErrorMessage, inv.CreatedAt, inv.UpdatedAt)
	return r.mapErr(err)
}

func scanInvocation(scan func(dest ...any) error) (domain.ToolInvocation, error) {
	var inv domain.ToolInvocation
	var src, gate, st string
	var artifactID sql.NullString
	if err := scan(&inv.ID, &inv.TurnID, &inv.RoomID, &inv.ActorID, &inv.CallID, &inv.ToolName, &src, &inv.ArgsJSON, &gate, &inv.DecisionReason, &st, &inv.ResultSummary, &artifactID, &inv.ErrorMessage, &inv.CreatedAt, &inv.UpdatedAt); err != nil {
		return domain.ToolInvocation{}, err
	}
	inv.ToolSource = domain.ToolSource(src)
	inv.GateMode = domain.ApprovalMode(gate)
	inv.Status = domain.ToolStatus(st)
	if artifactID.Valid {
		inv.ArtifactID = artifactID.String
	}
	return inv, nil
}

func (r toolRepo) GetInvocation(ctx context.Context, id string) (domain.ToolInvocation, error) {
	q := r.d.Rebind(`SELECT id, turn_id, room_id, actor_id, call_id, tool_name, tool_source, args_json, gate_mode, decision_reason, status, result_summary, artifact_id, error_message, created_at, updated_at FROM tool_invocations WHERE id = ?`)
	inv, err := scanInvocation(r.q.QueryRowContext(ctx, q, id).Scan)
	if err != nil {
		return domain.ToolInvocation{}, r.mapErr(err)
	}
	return inv, nil
}

func (r toolRepo) UpdateInvocation(ctx context.Context, inv domain.ToolInvocation) error {
	inv.UpdatedAt = domain.NowMS()
	var artifactArg sql.NullString
	if inv.ArtifactID != "" {
		artifactArg = sql.NullString{String: inv.ArtifactID, Valid: true}
	}
	q := r.d.Rebind(`UPDATE tool_invocations SET status = ?, decision_reason = ?, result_summary = ?, artifact_id = ?, error_message = ?, updated_at = ? WHERE id = ?`)
	res, err := r.q.ExecContext(ctx, q, string(inv.Status), inv.DecisionReason, inv.ResultSummary, artifactArg, inv.ErrorMessage, inv.UpdatedAt, inv.ID)
	if err != nil {
		return r.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r toolRepo) ListInvocationsByTurn(ctx context.Context, turnID string) ([]domain.ToolInvocation, error) {
	rows, err := r.q.QueryContext(ctx, r.d.Rebind(`SELECT id, turn_id, room_id, actor_id, call_id, tool_name, tool_source, args_json, gate_mode, decision_reason, status, result_summary, artifact_id, error_message, created_at, updated_at FROM tool_invocations WHERE turn_id = ? ORDER BY created_at ASC`), turnID)
	if err != nil {
		return nil, r.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.ToolInvocation
	for rows.Next() {
		inv, err := scanInvocation(rows.Scan)
		if err != nil {
			return nil, r.mapErr(err)
		}
		out = append(out, inv)
	}
	return out, r.mapErr(rows.Err())
}

func (r toolRepo) CreateQuestion(ctx context.Context, uq domain.UserQuestion) error {
	if uq.ID == "" {
		uq.ID = domain.NewID()
	}
	if uq.CreatedAt == 0 {
		uq.CreatedAt = domain.NowMS()
	}
	choicesJSON, err := marshalJSONString(uq.Choices, `[]`)
	if err != nil {
		return fmt.Errorf("%w: invalid choices: %v", storage.ErrInvalidInput, err)
	}
	var answered sql.NullInt64
	if uq.AnsweredAt != nil {
		answered = sql.NullInt64{Int64: *uq.AnsweredAt, Valid: true}
	}
	q := r.d.Rebind(`INSERT INTO user_questions (id, turn_id, room_id, actor_id, prompt, choices_json, allow_freeform, status, answer_text, created_at, answered_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err = r.q.ExecContext(ctx, q, uq.ID, uq.TurnID, uq.RoomID, uq.ActorID, uq.Prompt, choicesJSON, boolToInt(uq.AllowFreeform), string(uq.Status), uq.AnswerText, uq.CreatedAt, answered)
	return r.mapErr(err)
}

func scanQuestion(scan func(dest ...any) error) (domain.UserQuestion, error) {
	var uq domain.UserQuestion
	var choicesJSON, st string
	var allowInt int
	var answered sql.NullInt64
	if err := scan(&uq.ID, &uq.TurnID, &uq.RoomID, &uq.ActorID, &uq.Prompt, &choicesJSON, &allowInt, &st, &uq.AnswerText, &uq.CreatedAt, &answered); err != nil {
		return domain.UserQuestion{}, err
	}
	_ = json.Unmarshal([]byte(choicesJSON), &uq.Choices)
	uq.AllowFreeform = allowInt != 0
	uq.Status = domain.QuestionStatus(st)
	if answered.Valid {
		v := answered.Int64
		uq.AnsweredAt = &v
	}
	return uq, nil
}

func (r toolRepo) GetQuestion(ctx context.Context, id string) (domain.UserQuestion, error) {
	q := r.d.Rebind(`SELECT id, turn_id, room_id, actor_id, prompt, choices_json, allow_freeform, status, answer_text, created_at, answered_at FROM user_questions WHERE id = ?`)
	uq, err := scanQuestion(r.q.QueryRowContext(ctx, q, id).Scan)
	if err != nil {
		return domain.UserQuestion{}, r.mapErr(err)
	}
	return uq, nil
}

func (r toolRepo) UpdateQuestion(ctx context.Context, uq domain.UserQuestion) error {
	var answered sql.NullInt64
	if uq.AnsweredAt != nil {
		answered = sql.NullInt64{Int64: *uq.AnsweredAt, Valid: true}
	}
	q := r.d.Rebind(`UPDATE user_questions SET status = ?, answer_text = ?, answered_at = ? WHERE id = ?`)
	res, err := r.q.ExecContext(ctx, q, string(uq.Status), uq.AnswerText, answered, uq.ID)
	if err != nil {
		return r.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r toolRepo) ListQuestionsByTurn(ctx context.Context, turnID string) ([]domain.UserQuestion, error) {
	rows, err := r.q.QueryContext(ctx, r.d.Rebind(`SELECT id, turn_id, room_id, actor_id, prompt, choices_json, allow_freeform, status, answer_text, created_at, answered_at FROM user_questions WHERE turn_id = ? ORDER BY created_at ASC`), turnID)
	if err != nil {
		return nil, r.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.UserQuestion
	for rows.Next() {
		uq, err := scanQuestion(rows.Scan)
		if err != nil {
			return nil, r.mapErr(err)
		}
		out = append(out, uq)
	}
	return out, r.mapErr(rows.Err())
}

// --- Memories & Value Jobs ---

type memoryRepo struct{ repoBase }

func (r memoryRepo) CreateMemory(ctx context.Context, m domain.Memory) error {
	if m.ID == "" {
		m.ID = domain.NewID()
	}
	if m.Status == "" {
		m.Status = domain.MemoryStatusActive
	}
	if m.Revision <= 0 {
		m.Revision = 1
	}
	now := domain.NowMS()
	if m.CreatedAt == 0 {
		m.CreatedAt = now
	}
	if m.OccurredAt == 0 {
		m.OccurredAt = m.CreatedAt
	}
	obsJSON, err := marshalJSONString(m.ObservedBy, `[]`)
	if err != nil {
		return fmt.Errorf("%w: invalid observed_by: %v", storage.ErrInvalidInput, err)
	}
	dim, blob := encodeEmbedding(m.Embedding)
	if dim > 0 {
		m.EmbeddingDim = dim
	}
	q := r.d.Rebind(`INSERT INTO memories (id, room_id, owner_id, source_turn_id, kind, content, observed_by_json, occurred_at, pinned, status, revision, embedding_model, embedding_dim, embedding_blob, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err = r.q.ExecContext(ctx, q, m.ID, m.RoomID, m.OwnerID, m.SourceTurnID, string(m.Kind), m.Content, obsJSON, m.OccurredAt, boolToInt(m.Pinned), string(m.Status), m.Revision, m.EmbeddingModel, m.EmbeddingDim, blob, m.CreatedAt)
	return r.mapErr(err)
}

func scanMemory(scan func(dest ...any) error) (domain.Memory, error) {
	var m domain.Memory
	var kind, obsJSON, st string
	var pinnedInt int
	var blob []byte
	if err := scan(&m.ID, &m.RoomID, &m.OwnerID, &m.SourceTurnID, &kind, &m.Content, &obsJSON, &m.OccurredAt, &pinnedInt, &st, &m.Revision, &m.EmbeddingModel, &m.EmbeddingDim, &blob, &m.CreatedAt); err != nil {
		return domain.Memory{}, err
	}
	m.Kind = domain.MemoryKind(kind)
	m.Status = domain.MemoryStatus(st)
	m.Pinned = pinnedInt != 0
	_ = json.Unmarshal([]byte(obsJSON), &m.ObservedBy)
	m.Embedding = decodeEmbedding(m.EmbeddingDim, blob)
	return m, nil
}

func (r memoryRepo) GetMemory(ctx context.Context, id string) (domain.Memory, error) {
	q := r.d.Rebind(`SELECT id, room_id, owner_id, source_turn_id, kind, content, observed_by_json, occurred_at, pinned, status, revision, embedding_model, embedding_dim, embedding_blob, created_at FROM memories WHERE id = ?`)
	m, err := scanMemory(r.q.QueryRowContext(ctx, q, id).Scan)
	if err != nil {
		return domain.Memory{}, r.mapErr(err)
	}
	return m, nil
}

func (r memoryRepo) ListMemories(ctx context.Context, roomID, ownerID, observerID string, limit int) ([]domain.Memory, error) {
	query := `SELECT id, room_id, owner_id, source_turn_id, kind, content, observed_by_json, occurred_at, pinned, status, revision, embedding_model, embedding_dim, embedding_blob, created_at FROM memories WHERE status = 'active'`
	var args []any
	if roomID != "" {
		query += ` AND room_id = ?`
		args = append(args, roomID)
	}
	if ownerID != "" {
		query += ` AND owner_id = ?`
		args = append(args, ownerID)
	}
	query += ` ORDER BY occurred_at DESC, created_at DESC`
	rows, err := r.q.QueryContext(ctx, r.d.Rebind(query), args...)
	if err != nil {
		return nil, r.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Memory
	for rows.Next() {
		m, err := scanMemory(rows.Scan)
		if err != nil {
			return nil, r.mapErr(err)
		}
		if observerID != "" && !slices.Contains(m.ObservedBy, observerID) {
			continue
		}
		out = append(out, m)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, r.mapErr(rows.Err())
}

func (r memoryRepo) UpdateMemory(ctx context.Context, m domain.Memory) error {
	obsJSON, err := marshalJSONString(m.ObservedBy, `[]`)
	if err != nil {
		return fmt.Errorf("%w: invalid observed_by: %v", storage.ErrInvalidInput, err)
	}
	dim, blob := encodeEmbedding(m.Embedding)
	if dim > 0 {
		m.EmbeddingDim = dim
	}
	q := r.d.Rebind(`UPDATE memories SET content = ?, observed_by_json = ?, pinned = ?, status = ?, revision = ?, embedding_model = ?, embedding_dim = ?, embedding_blob = ? WHERE id = ?`)
	res, err := r.q.ExecContext(ctx, q, m.Content, obsJSON, boolToInt(m.Pinned), string(m.Status), m.Revision, m.EmbeddingModel, m.EmbeddingDim, blob, m.ID)
	if err != nil {
		return r.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r memoryRepo) CreateValueJob(ctx context.Context, job domain.ValueJob) error {
	if job.ID == "" {
		job.ID = domain.NewID()
	}
	if job.Status == "" {
		job.Status = domain.ValueJobQueued
	}
	now := domain.NowMS()
	if job.CreatedAt == 0 {
		job.CreatedAt = now
	}
	if job.UpdatedAt == 0 {
		job.UpdatedAt = job.CreatedAt
	}
	if job.NextRunAt == 0 {
		job.NextRunAt = now
	}
	if strings.TrimSpace(job.CandidatesJSON) == "" {
		job.CandidatesJSON = "[]"
	}
	q := r.d.Rebind(`INSERT INTO value_jobs (id, room_id, character_id, source_turn_id, base_values_revision, candidates_json, status, attempts, last_error, next_run_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err := r.q.ExecContext(ctx, q, job.ID, job.RoomID, job.CharacterID, job.SourceTurnID, job.BaseValuesRevision, job.CandidatesJSON, string(job.Status), job.Attempts, job.LastError, job.NextRunAt, job.CreatedAt, job.UpdatedAt)
	return r.mapErr(err)
}

func (r memoryRepo) ClaimNextValueJob(ctx context.Context, nowMS int64) (*domain.ValueJob, error) {
	if nowMS == 0 {
		nowMS = domain.NowMS()
	}
	run := func(exec Execer) (*domain.ValueJob, error) {
		sel := `SELECT id, room_id, character_id, source_turn_id, base_values_revision, candidates_json, status, attempts, last_error, next_run_at, created_at, updated_at FROM value_jobs WHERE status = 'queued' AND next_run_at <= ? ORDER BY next_run_at ASC, created_at ASC LIMIT 1`
		if lock := strings.TrimSpace(r.d.ClaimValueJobLockClause()); lock != "" {
			sel += " " + lock
		}
		var job domain.ValueJob
		var st string
		err := exec.QueryRowContext(ctx, r.d.Rebind(sel), nowMS).Scan(
			&job.ID, &job.RoomID, &job.CharacterID, &job.SourceTurnID, &job.BaseValuesRevision,
			&job.CandidatesJSON, &st, &job.Attempts, &job.LastError, &job.NextRunAt, &job.CreatedAt, &job.UpdatedAt,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, r.mapErr(err)
		}
		job.Status = domain.ValueJobRunning
		job.UpdatedAt = nowMS
		upd := r.d.Rebind(`UPDATE value_jobs SET status = ?, updated_at = ? WHERE id = ?`)
		if _, err := exec.ExecContext(ctx, upd, string(job.Status), job.UpdatedAt, job.ID); err != nil {
			return nil, r.mapErr(err)
		}
		return &job, nil
	}

	if r.tx != nil {
		return run(r.tx)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, r.mapErr(err)
	}
	job, err := run(tx)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, r.mapErr(err)
	}
	return job, nil
}

func (r memoryRepo) UpdateValueJob(ctx context.Context, job domain.ValueJob) error {
	job.UpdatedAt = domain.NowMS()
	if strings.TrimSpace(job.CandidatesJSON) == "" {
		job.CandidatesJSON = "[]"
	}
	q := r.d.Rebind(`UPDATE value_jobs SET candidates_json = ?, status = ?, attempts = ?, last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ?`)
	res, err := r.q.ExecContext(ctx, q, job.CandidatesJSON, string(job.Status), job.Attempts, job.LastError, job.NextRunAt, job.UpdatedAt, job.ID)
	if err != nil {
		return r.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

// --- Knowledge ---

type knowledgeRepo struct{ repoBase }

func (r knowledgeRepo) CreateDoc(ctx context.Context, doc domain.KnowledgeDoc) (domain.KnowledgeDoc, error) {
	if doc.Title == "" || doc.Scope == "" {
		return domain.KnowledgeDoc{}, fmt.Errorf("%w: title and scope are required", storage.ErrInvalidInput)
	}
	if doc.ID == "" {
		doc.ID = domain.NewID()
	}
	if doc.MediaType == "" {
		doc.MediaType = "text/plain"
	}
	if doc.Revision <= 0 {
		doc.Revision = 1
	}
	if doc.Status == "" {
		doc.Status = domain.KnowledgeStatusActive
	}
	now := domain.NowMS()
	if doc.CreatedAt == 0 {
		doc.CreatedAt = now
	}
	if doc.UpdatedAt == 0 {
		doc.UpdatedAt = doc.CreatedAt
	}
	q := r.d.Rebind(`INSERT INTO knowledge_docs (id, scope, title, source_uri, media_type, content, content_sha256, revision, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if _, err := r.q.ExecContext(ctx, q, doc.ID, doc.Scope, doc.Title, doc.SourceURI, doc.MediaType, doc.Content, doc.ContentSHA256, doc.Revision, string(doc.Status), doc.CreatedAt, doc.UpdatedAt); err != nil {
		return domain.KnowledgeDoc{}, r.mapErr(err)
	}
	return doc, nil
}

func scanKnowledgeDoc(scan func(dest ...any) error) (domain.KnowledgeDoc, error) {
	var d domain.KnowledgeDoc
	var st string
	if err := scan(&d.ID, &d.Scope, &d.Title, &d.SourceURI, &d.MediaType, &d.Content, &d.ContentSHA256, &d.Revision, &st, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return domain.KnowledgeDoc{}, err
	}
	d.Status = domain.KnowledgeStatus(st)
	return d, nil
}

func (r knowledgeRepo) GetDoc(ctx context.Context, id string) (domain.KnowledgeDoc, error) {
	q := r.d.Rebind(`SELECT id, scope, title, source_uri, media_type, content, content_sha256, revision, status, created_at, updated_at FROM knowledge_docs WHERE id = ? AND status != 'deleted'`)
	d, err := scanKnowledgeDoc(r.q.QueryRowContext(ctx, q, id).Scan)
	if err != nil {
		return domain.KnowledgeDoc{}, r.mapErr(err)
	}
	return d, nil
}

func (r knowledgeRepo) ListDocs(ctx context.Context, scope string) ([]domain.KnowledgeDoc, error) {
	var rows *sql.Rows
	var err error
	if scope != "" {
		q := r.d.Rebind(`SELECT id, scope, title, source_uri, media_type, content, content_sha256, revision, status, created_at, updated_at FROM knowledge_docs WHERE scope = ? AND status = 'active' ORDER BY updated_at DESC`)
		rows, err = r.q.QueryContext(ctx, q, scope)
	} else {
		q := r.d.Rebind(`SELECT id, scope, title, source_uri, media_type, content, content_sha256, revision, status, created_at, updated_at FROM knowledge_docs WHERE status = 'active' ORDER BY updated_at DESC`)
		rows, err = r.q.QueryContext(ctx, q)
	}
	if err != nil {
		return nil, r.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.KnowledgeDoc
	for rows.Next() {
		d, err := scanKnowledgeDoc(rows.Scan)
		if err != nil {
			return nil, r.mapErr(err)
		}
		out = append(out, d)
	}
	return out, r.mapErr(rows.Err())
}

func (r knowledgeRepo) UpdateDoc(ctx context.Context, doc domain.KnowledgeDoc) (domain.KnowledgeDoc, error) {
	doc.UpdatedAt = domain.NowMS()
	q := r.d.Rebind(`UPDATE knowledge_docs SET scope = ?, title = ?, source_uri = ?, media_type = ?, content = ?, content_sha256 = ?, revision = ?, status = ?, updated_at = ? WHERE id = ? AND status != 'deleted'`)
	res, err := r.q.ExecContext(ctx, q, doc.Scope, doc.Title, doc.SourceURI, doc.MediaType, doc.Content, doc.ContentSHA256, doc.Revision, string(doc.Status), doc.UpdatedAt, doc.ID)
	if err != nil {
		return domain.KnowledgeDoc{}, r.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.KnowledgeDoc{}, storage.ErrNotFound
	}
	return r.GetDoc(ctx, doc.ID)
}

func (r knowledgeRepo) DeleteDoc(ctx context.Context, id string, updatedAt int64) error {
	if updatedAt == 0 {
		updatedAt = domain.NowMS()
	}
	q := r.d.Rebind(`UPDATE knowledge_docs SET status = 'deleted', updated_at = ? WHERE id = ? AND status != 'deleted'`)
	res, err := r.q.ExecContext(ctx, q, updatedAt, id)
	if err != nil {
		return r.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r knowledgeRepo) ReplaceChunks(ctx context.Context, docID string, chunks []domain.KnowledgeChunk) error {
	run := func(exec Execer) error {
		if _, err := exec.ExecContext(ctx, r.d.Rebind(`DELETE FROM knowledge_chunks WHERE doc_id = ?`), docID); err != nil {
			return r.mapErr(err)
		}
		ins := r.d.Rebind(`INSERT INTO knowledge_chunks (id, doc_id, doc_revision, chunk_ordinal, char_offset, text, embedding_model, embedding_dim, embedding_blob) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		for _, ch := range chunks {
			if ch.ID == "" {
				ch.ID = domain.NewID()
			}
			ch.DocID = docID
			dim, blob := encodeEmbedding(ch.Embedding)
			if dim > 0 {
				ch.EmbeddingDim = dim
			}
			if _, err := exec.ExecContext(ctx, ins, ch.ID, ch.DocID, ch.DocRevision, ch.ChunkOrdinal, ch.CharOffset, ch.Text, ch.EmbeddingModel, ch.EmbeddingDim, blob); err != nil {
				return r.mapErr(err)
			}
		}
		return nil
	}

	if r.tx != nil {
		return run(r.tx)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return r.mapErr(err)
	}
	if err := run(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return r.mapErr(tx.Commit())
}

func scanKnowledgeChunk(scan func(dest ...any) error) (domain.KnowledgeChunk, error) {
	var c domain.KnowledgeChunk
	var blob []byte
	if err := scan(&c.ID, &c.DocID, &c.DocRevision, &c.ChunkOrdinal, &c.CharOffset, &c.Text, &c.EmbeddingModel, &c.EmbeddingDim, &blob); err != nil {
		return domain.KnowledgeChunk{}, err
	}
	c.Embedding = decodeEmbedding(c.EmbeddingDim, blob)
	return c, nil
}

func (r knowledgeRepo) ListChunksByDoc(ctx context.Context, docID string) ([]domain.KnowledgeChunk, error) {
	rows, err := r.q.QueryContext(ctx, r.d.Rebind(`SELECT id, doc_id, doc_revision, chunk_ordinal, char_offset, text, embedding_model, embedding_dim, embedding_blob FROM knowledge_chunks WHERE doc_id = ? ORDER BY chunk_ordinal ASC`), docID)
	if err != nil {
		return nil, r.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.KnowledgeChunk
	for rows.Next() {
		c, err := scanKnowledgeChunk(rows.Scan)
		if err != nil {
			return nil, r.mapErr(err)
		}
		out = append(out, c)
	}
	return out, r.mapErr(rows.Err())
}

func (r knowledgeRepo) GetChunk(ctx context.Context, id string) (domain.KnowledgeChunk, error) {
	q := r.d.Rebind(`SELECT id, doc_id, doc_revision, chunk_ordinal, char_offset, text, embedding_model, embedding_dim, embedding_blob FROM knowledge_chunks WHERE id = ?`)
	c, err := scanKnowledgeChunk(r.q.QueryRowContext(ctx, q, id).Scan)
	if err != nil {
		return domain.KnowledgeChunk{}, r.mapErr(err)
	}
	return c, nil
}

// --- Outbox ---

type outboxRepo struct{ repoBase }

func (r outboxRepo) Enqueue(ctx context.Context, entry domain.OutboxEntry) (int64, error) {
	if entry.Status == "" {
		entry.Status = domain.OutboxStatusPending
	}
	if entry.PayloadJSON == "" {
		entry.PayloadJSON = "{}"
	}
	now := domain.NowMS()
	if entry.CreatedAt == 0 {
		entry.CreatedAt = now
	}
	if entry.NextAttemptAt == 0 {
		entry.NextAttemptAt = now
	}
	seq, err := r.d.EnqueueOutbox(ctx, r.q, entry)
	return seq, r.mapErr(err)
}

func (r outboxRepo) ListPending(ctx context.Context, nowMS int64, limit int) ([]domain.OutboxEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	if nowMS == 0 {
		nowMS = domain.NowMS()
	}
	q := r.d.Rebind(`SELECT seq, entity_type, entity_id, op, revision, payload_json, status, attempts, next_attempt_at, last_error, created_at FROM index_outbox WHERE status IN ('pending', 'retry') AND next_attempt_at <= ? ORDER BY seq ASC LIMIT ?`)
	rows, err := r.q.QueryContext(ctx, q, nowMS, limit)
	if err != nil {
		return nil, r.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.OutboxEntry
	for rows.Next() {
		var e domain.OutboxEntry
		var op, st string
		if err := rows.Scan(&e.Seq, &e.EntityType, &e.EntityID, &op, &e.Revision, &e.PayloadJSON, &st, &e.Attempts, &e.NextAttemptAt, &e.LastError, &e.CreatedAt); err != nil {
			return nil, r.mapErr(err)
		}
		e.Op = domain.OutboxOp(op)
		e.Status = domain.OutboxStatus(st)
		out = append(out, e)
	}
	return out, r.mapErr(rows.Err())
}

func (r outboxRepo) MarkDone(ctx context.Context, seq int64) error {
	res, err := r.q.ExecContext(ctx, r.d.Rebind(`UPDATE index_outbox SET status = 'done', last_error = '' WHERE seq = ?`), seq)
	if err != nil {
		return r.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r outboxRepo) MarkRetry(ctx context.Context, seq int64, status domain.OutboxStatus, nextAttemptAt int64, lastErr string) error {
	res, err := r.q.ExecContext(ctx, r.d.Rebind(`UPDATE index_outbox SET status = ?, attempts = attempts + 1, next_attempt_at = ?, last_error = ? WHERE seq = ?`), string(status), nextAttemptAt, lastErr, seq)
	if err != nil {
		return r.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

// --- Artifacts ---

type artifactRepo struct{ repoBase }

func (r artifactRepo) Create(ctx context.Context, a domain.Artifact) error {
	if a.ID == "" {
		a.ID = domain.NewID()
	}
	if a.SourceMetaJSON == "" {
		a.SourceMetaJSON = "{}"
	}
	now := domain.NowMS()
	if a.CreatedAt == 0 {
		a.CreatedAt = now
	}
	if a.LastAccessedAt == 0 {
		a.LastAccessedAt = a.CreatedAt
	}
	q := r.d.Rebind(`INSERT INTO artifacts (id, kind, media_type, byte_size, sha256, blob_key, source_meta_json, pinned, created_at, last_accessed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err := r.q.ExecContext(ctx, q, a.ID, string(a.Kind), a.MediaType, a.ByteSize, a.SHA256, a.BlobKey, a.SourceMetaJSON, boolToInt(a.Pinned), a.CreatedAt, a.LastAccessedAt)
	return r.mapErr(err)
}

func (r artifactRepo) Get(ctx context.Context, id string) (domain.Artifact, error) {
	var a domain.Artifact
	var kind string
	var pinnedInt int
	q := r.d.Rebind(`SELECT id, kind, media_type, byte_size, sha256, blob_key, source_meta_json, pinned, created_at, last_accessed_at FROM artifacts WHERE id = ?`)
	err := r.q.QueryRowContext(ctx, q, id).Scan(&a.ID, &kind, &a.MediaType, &a.ByteSize, &a.SHA256, &a.BlobKey, &a.SourceMetaJSON, &pinnedInt, &a.CreatedAt, &a.LastAccessedAt)
	if err != nil {
		return domain.Artifact{}, r.mapErr(err)
	}
	a.Kind = domain.ArtifactKind(kind)
	a.Pinned = pinnedInt != 0
	return a, nil
}

func (r artifactRepo) Touch(ctx context.Context, id string, accessedAt int64) error {
	if accessedAt == 0 {
		accessedAt = domain.NowMS()
	}
	res, err := r.q.ExecContext(ctx, r.d.Rebind(`UPDATE artifacts SET last_accessed_at = ? WHERE id = ?`), accessedAt, id)
	if err != nil {
		return r.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r artifactRepo) Delete(ctx context.Context, id string) error {
	res, err := r.q.ExecContext(ctx, r.d.Rebind(`DELETE FROM artifacts WHERE id = ?`), id)
	if err != nil {
		return r.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return storage.ErrNotFound
	}
	return nil
}

func (r artifactRepo) ListByKind(ctx context.Context, kind domain.ArtifactKind) ([]domain.Artifact, error) {
	q := r.d.Rebind(`SELECT id, kind, media_type, byte_size, sha256, blob_key, source_meta_json, pinned, created_at, last_accessed_at FROM artifacts WHERE kind = ? ORDER BY created_at ASC, id ASC`)
	rows, err := r.q.QueryContext(ctx, q, string(kind))
	if err != nil {
		return nil, r.mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Artifact
	for rows.Next() {
		var a domain.Artifact
		var k string
		var pinnedInt int
		if err := rows.Scan(&a.ID, &k, &a.MediaType, &a.ByteSize, &a.SHA256, &a.BlobKey, &a.SourceMetaJSON, &pinnedInt, &a.CreatedAt, &a.LastAccessedAt); err != nil {
			return nil, r.mapErr(err)
		}
		a.Kind = domain.ArtifactKind(k)
		a.Pinned = pinnedInt != 0
		out = append(out, a)
	}
	return out, r.mapErr(rows.Err())
}

func (r artifactRepo) ListLoomRootCandidates(ctx context.Context) (storage.LoomRootCandidates, error) {
	var res storage.LoomRootCandidates

	invQ := r.d.Rebind(`
		SELECT DISTINCT ti.artifact_id
		FROM tool_invocations ti
		INNER JOIN turns t ON t.id = ti.turn_id
		WHERE t.status IN ('running', 'awaiting_approval', 'awaiting_user')
		  AND ti.artifact_id IS NOT NULL
		  AND ti.artifact_id <> ''
	`)
	invRows, err := r.q.QueryContext(ctx, invQ)
	if err != nil {
		return res, r.mapErr(err)
	}
	for invRows.Next() {
		var id string
		if err := invRows.Scan(&id); err != nil {
			_ = invRows.Close()
			return res, r.mapErr(err)
		}
		res.ActiveInvocationArtifactIDs = append(res.ActiveInvocationArtifactIDs, id)
	}
	if err := invRows.Err(); err != nil {
		_ = invRows.Close()
		return res, r.mapErr(err)
	}
	_ = invRows.Close()

	cpQ := r.d.Rebind(`
		SELECT checkpoint_json
		FROM turns
		WHERE status IN ('running', 'awaiting_approval', 'awaiting_user')
		  AND checkpoint_json LIKE '%loom://%'
	`)
	cpRows, err := r.q.QueryContext(ctx, cpQ)
	if err != nil {
		return res, r.mapErr(err)
	}
	for cpRows.Next() {
		var text string
		if err := cpRows.Scan(&text); err != nil {
			_ = cpRows.Close()
			return res, r.mapErr(err)
		}
		res.ReferencedTexts = append(res.ReferencedTexts, text)
	}
	if err := cpRows.Err(); err != nil {
		_ = cpRows.Close()
		return res, r.mapErr(err)
	}
	_ = cpRows.Close()

	msgQ := r.d.Rebind(`
		SELECT m.content
		FROM messages m
		INNER JOIN rooms r ON r.id = m.room_id
		WHERE r.deleted_at IS NULL
		  AND m.content LIKE '%loom://%'
	`)
	msgRows, err := r.q.QueryContext(ctx, msgQ)
	if err != nil {
		return res, r.mapErr(err)
	}
	for msgRows.Next() {
		var text string
		if err := msgRows.Scan(&text); err != nil {
			_ = msgRows.Close()
			return res, r.mapErr(err)
		}
		res.ReferencedTexts = append(res.ReferencedTexts, text)
	}
	if err := msgRows.Err(); err != nil {
		_ = msgRows.Close()
		return res, r.mapErr(err)
	}
	_ = msgRows.Close()

	return res, nil
}

func (r artifactRepo) ClearToolInvocationRefs(ctx context.Context, artifactID string) error {
	res, err := r.q.ExecContext(ctx, r.d.Rebind(`UPDATE tool_invocations SET artifact_id = NULL WHERE artifact_id = ?`), artifactID)
	if err != nil {
		return r.mapErr(err)
	}
	_ = res
	return nil
}
