package app

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/snowmerak/q/v2/pkg/app/config"
)

type apiErrorBody struct {
	Error apiErrorDetail `json:"error"`
}

type apiErrorDetail struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

// Handler returns the HTTP handler for /api/v1 endpoints. Unmatched /api/ routes
// return structured JSON 404 instead of falling back to any SPA HTML.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", a.handleHealth)
	mux.HandleFunc("GET /api/v1/config", a.handleGetConfig)
	mux.HandleFunc("PUT /api/v1/config", a.handlePutConfig)
	mux.HandleFunc("GET /api/v1/models", a.handleGetModels)
	mux.HandleFunc("POST /api/v1/models/refresh", a.handleRefreshModels)
	mux.HandleFunc("GET /api/v1/decisions/models", a.handleGetDecisionModels)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if _, err := uuid.Parse(reqID); err != nil {
			reqID = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", reqID)

		if strings.HasPrefix(r.URL.Path, "/api/") {
			h, pattern := mux.Handler(r)
			if pattern == "" {
				writeError(w, http.StatusNotFound, "not_found", "API endpoint not found", false, nil)
				return
			}
			h.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "frontend assets are not yet built", false, nil)
	})
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rdbStatus := "ready"
	if a.rdb == nil || a.rdb.Ping(ctx) != nil {
		rdbStatus = "degraded"
	}
	searchStatus := "ready"
	if a.search == nil {
		searchStatus = "degraded"
	} else if _, _, err := a.search.Counts(ctx); err != nil {
		searchStatus = "degraded"
	}
	blobStatus := "ready"
	if a.blob == nil {
		blobStatus = "degraded"
	}

	a.providerMu.RLock()
	degradedReason := a.degradedReason
	a.providerMu.RUnlock()

	overall := "ready"
	if rdbStatus != "ready" || searchStatus != "ready" || blobStatus != "ready" || degradedReason != "" {
		overall = "degraded"
	}
	resp := map[string]any{
		"status": overall,
		"rdb":    rdbStatus,
		"search": searchStatus,
		"blob":   blobStatus,
	}
	if degradedReason != "" {
		resp["reason"] = degradedReason
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.currentConfig().Redact())
}

func (a *App) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", "failed to read request body", false, nil)
		return
	}
	next, err := config.Parse(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error(), false, nil)
		return
	}
	prev := a.currentConfig()
	if err := config.Save(a.configPath, next); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to save config", true, nil)
		return
	}
	if a.watcher != nil {
		a.watcher.Update(next)
	}
	a.reloadProviders(r.Context(), next)
	restartFields := config.DiffRestartRequired(prev, next)
	if restartFields == nil {
		restartFields = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"applied":                 true,
		"restart_required_fields": restartFields,
	})
}

func (a *App) handleGetModels(w http.ResponseWriter, r *http.Request) {
	models, err := a.listModels(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", err.Error(), true, nil)
		return
	}
	cfg := a.currentConfig()
	writeJSON(w, http.StatusOK, map[string]any{
		"models":        models,
		"default_roles": cfg.LLM.Roles,
	})
}

func (a *App) handleRefreshModels(w http.ResponseWriter, r *http.Request) {
	models, err := a.refreshModels(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", err.Error(), true, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"refreshed_at": time.Now().UTC().UnixMilli(),
		"count":        len(models),
	})
}

func (a *App) handleGetDecisionModels(w http.ResponseWriter, _ *http.Request) {
	models, def := a.listDecisionModels()
	writeJSON(w, http.StatusOK, map[string]any{
		"models":           models,
		"default_model_id": def,
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string, retryable bool, details map[string]any) {
	writeJSON(w, status, apiErrorBody{
		Error: apiErrorDetail{
			Code:      code,
			Message:   message,
			Retryable: retryable,
			Details:   details,
		},
	})
}
