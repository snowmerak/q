package app

import (
	"net/http"
	"strings"

	"github.com/snowmerak/q/v2/pkg/domain"
)

type personaInput struct {
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	AvatarArtifactID string `json:"avatar_artifact_id,omitempty"`
	IsDefault        bool   `json:"is_default"`
}

type personaPatch struct {
	ExpectedVersion  *int64  `json:"expected_version"`
	Name             *string `json:"name,omitempty"`
	Description      *string `json:"description,omitempty"`
	AvatarArtifactID *string `json:"avatar_artifact_id,omitempty"`
	IsDefault        *bool   `json:"is_default,omitempty"`
}

type characterInput struct {
	Name                   string               `json:"name"`
	AvatarArtifactID       string               `json:"avatar_artifact_id,omitempty"`
	Personality            string               `json:"personality,omitempty"`
	Background             string               `json:"background,omitempty"`
	Appearance             string               `json:"appearance,omitempty"`
	InitialValues          []string             `json:"initial_values,omitempty"`
	InitialState           *domain.InitialState `json:"initial_state,omitempty"`
	ModelPrefs             *domain.ModelPrefs   `json:"model_prefs,omitempty"`
	ApprovalPolicyOverride *domain.ApprovalMode `json:"approval_policy_override,omitempty"`
}

type characterPatch struct {
	ExpectedVersion        *int64               `json:"expected_version"`
	Name                   *string              `json:"name,omitempty"`
	AvatarArtifactID       *string              `json:"avatar_artifact_id,omitempty"`
	Personality            *string              `json:"personality,omitempty"`
	Background             *string              `json:"background,omitempty"`
	Appearance             *string              `json:"appearance,omitempty"`
	InitialValues          *[]string            `json:"initial_values,omitempty"`
	InitialState           *domain.InitialState `json:"initial_state,omitempty"`
	ModelPrefs             *domain.ModelPrefs   `json:"model_prefs,omitempty"`
	ApprovalPolicyOverride *domain.ApprovalMode `json:"approval_policy_override,omitempty"`
}

func (a *App) handleListPersonas(w http.ResponseWriter, r *http.Request) {
	items, err := a.rdb.Personas().List(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if items == nil {
		items = []domain.Persona{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *App) handleCreatePersona(w http.ResponseWriter, r *http.Request) {
	var in personaInput
	if !decodeJSONBody(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "persona name is required", false, nil)
		return
	}
	created, err := a.rdb.Personas().Create(r.Context(), domain.Persona{
		Name:             in.Name,
		Description:      strings.TrimSpace(in.Description),
		AvatarArtifactID: strings.TrimSpace(in.AvatarArtifactID),
		IsDefault:        in.IsDefault,
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (a *App) handlePatchPersona(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var patch personaPatch
	if !decodeJSONBody(w, r, &patch) {
		return
	}
	if patch.ExpectedVersion == nil || *patch.ExpectedVersion < 1 {
		writeError(w, http.StatusBadRequest, "invalid_input", "expected_version >= 1 is required", false, nil)
		return
	}
	cur, err := a.rdb.Personas().Get(r.Context(), id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if cur.Version != *patch.ExpectedVersion {
		writeError(w, http.StatusConflict, "version_conflict", "persona version mismatch", false, nil)
		return
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "invalid_input", "persona name cannot be empty", false, nil)
			return
		}
		cur.Name = name
	}
	if patch.Description != nil {
		cur.Description = strings.TrimSpace(*patch.Description)
	}
	if patch.AvatarArtifactID != nil {
		cur.AvatarArtifactID = strings.TrimSpace(*patch.AvatarArtifactID)
	}
	if patch.IsDefault != nil {
		cur.IsDefault = *patch.IsDefault
	}
	updated, err := a.rdb.Personas().Update(r.Context(), cur)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (a *App) handleDeletePersona(w http.ResponseWriter, r *http.Request) {
	if err := a.rdb.Personas().Delete(r.Context(), r.PathValue("id"), domain.NowMS()); err != nil {
		writeDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleListCharacters(w http.ResponseWriter, r *http.Request) {
	items, err := a.rdb.Characters().List(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if items == nil {
		items = []domain.Character{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *App) handleCreateCharacter(w http.ResponseWriter, r *http.Request) {
	var in characterInput
	if !decodeJSONBody(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_input", "character name is required", false, nil)
		return
	}
	if in.ApprovalPolicyOverride != nil && !in.ApprovalPolicyOverride.Valid() {
		writeError(w, http.StatusBadRequest, "invalid_input", "invalid approval_policy_override", false, nil)
		return
	}
	created, err := a.rdb.Characters().Create(r.Context(), domain.Character{
		Name:                   in.Name,
		AvatarArtifactID:       strings.TrimSpace(in.AvatarArtifactID),
		Personality:            in.Personality,
		Background:             in.Background,
		Appearance:             in.Appearance,
		InitialValues:          in.InitialValues,
		InitialState:           in.InitialState,
		ModelPrefs:             in.ModelPrefs,
		ApprovalPolicyOverride: in.ApprovalPolicyOverride,
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (a *App) handleGetCharacter(w http.ResponseWriter, r *http.Request) {
	ch, err := a.rdb.Characters().Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

func (a *App) handlePatchCharacter(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var patch characterPatch
	if !decodeJSONBody(w, r, &patch) {
		return
	}
	if patch.ExpectedVersion == nil || *patch.ExpectedVersion < 1 {
		writeError(w, http.StatusBadRequest, "invalid_input", "expected_version >= 1 is required", false, nil)
		return
	}
	if patch.ApprovalPolicyOverride != nil && !patch.ApprovalPolicyOverride.Valid() {
		writeError(w, http.StatusBadRequest, "invalid_input", "invalid approval_policy_override", false, nil)
		return
	}
	cur, err := a.rdb.Characters().Get(r.Context(), id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if cur.Version != *patch.ExpectedVersion {
		writeError(w, http.StatusConflict, "version_conflict", "character version mismatch", false, nil)
		return
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "invalid_input", "character name cannot be empty", false, nil)
			return
		}
		cur.Name = name
	}
	if patch.AvatarArtifactID != nil {
		cur.AvatarArtifactID = strings.TrimSpace(*patch.AvatarArtifactID)
	}
	if patch.Personality != nil {
		cur.Personality = *patch.Personality
	}
	if patch.Background != nil {
		cur.Background = *patch.Background
	}
	if patch.Appearance != nil {
		cur.Appearance = *patch.Appearance
	}
	if patch.InitialValues != nil {
		cur.InitialValues = *patch.InitialValues
	}
	if patch.InitialState != nil {
		cur.InitialState = patch.InitialState
	}
	if patch.ModelPrefs != nil {
		cur.ModelPrefs = patch.ModelPrefs
	}
	if patch.ApprovalPolicyOverride != nil {
		cur.ApprovalPolicyOverride = patch.ApprovalPolicyOverride
	}
	updated, err := a.rdb.Characters().Update(r.Context(), cur)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (a *App) handleDeleteCharacter(w http.ResponseWriter, r *http.Request) {
	if err := a.rdb.Characters().Delete(r.Context(), r.PathValue("id"), domain.NowMS()); err != nil {
		writeDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
