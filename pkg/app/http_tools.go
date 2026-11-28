package app

import (
	"net/http"

	"github.com/snowmerak/q/v2/pkg/knowledge"
)

func (a *App) handleResolveApproval(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	invID := r.PathValue("invocationId")
	var req struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	inv, err := a.engine.ResolveApproval(r.Context(), roomID, invID, req.Decision, req.Reason)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

func (a *App) handleResolveQuestion(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	qID := r.PathValue("questionId")
	var req struct {
		AnswerText string `json:"answer_text"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	q, err := a.engine.ResolveQuestion(r.Context(), roomID, qID, req.AnswerText)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, q)
}

func (a *App) handleListKnowledge(w http.ResponseWriter, r *http.Request) {
	items, err := a.knowledge.ListDocs(r.Context(), r.URL.Query().Get("scope"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *App) handleCreateKnowledge(w http.ResponseWriter, r *http.Request) {
	var in knowledge.CreateDocInput
	if !decodeJSONBody(w, r, &in) {
		return
	}
	created, err := a.knowledge.CreateDoc(r.Context(), in)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (a *App) handlePatchKnowledge(w http.ResponseWriter, r *http.Request) {
	var in knowledge.UpdateDocInput
	if !decodeJSONBody(w, r, &in) {
		return
	}
	updated, err := a.knowledge.UpdateDoc(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (a *App) handleDeleteKnowledge(w http.ResponseWriter, r *http.Request) {
	if err := a.knowledge.DeleteDoc(r.Context(), r.PathValue("id")); err != nil {
		writeDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleSearchKnowledge(w http.ResponseWriter, r *http.Request) {
	var in knowledge.SearchInput
	if !decodeJSONBody(w, r, &in) {
		return
	}
	hits, err := a.knowledge.Search(r.Context(), in)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits})
}

func (a *App) handleListSkills(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": a.skills.ListSkills()})
}
