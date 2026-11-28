package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/snowmerak/q/v2/pkg/llm/providers/chatgpt"
)

// Authentication management is local-only. Q additionally wraps this surface
// in its Gateway bearer authentication and proxies it through Studio.
func (g *Gateway) handleChatGPTAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(remote)
	if err != nil || ip == nil || !ip.IsLoopback() {
		writeError(w, 403, errors.New("ChatGPT account management requires a loopback connection"))
		return
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host != "localhost" {
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if ip == nil || !ip.IsLoopback() {
			writeError(w, 403, errors.New("invalid local management host"))
			return
		}
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		value, err := url.Parse(origin)
		if err != nil || value.Host != r.Host || (value.Scheme != "http" && value.Scheme != "https") {
			writeError(w, 403, errors.New("cross-origin account management is not allowed"))
			return
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeError(w, 403, errors.New("cross-site account management is not allowed"))
		return
	}
	var target *route
	for _, entry := range g.order {
		if entry.id == r.PathValue("provider") {
			target = entry
			break
		}
	}
	if target == nil {
		writeError(w, 404, errors.New("provider does not exist"))
		return
	}
	provider, ok := target.provider.(*chatgpt.Provider)
	if !ok {
		writeError(w, 400, errors.New("provider does not use Sign in with ChatGPT"))
		return
	}
	if r.Method == http.MethodPost {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeError(w, 415, errors.New("account actions require application/json"))
			return
		}
		var request struct {
			NewAccount bool   `json:"new_account"`
			Profile    string `json:"profile"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeError(w, 400, err)
			return
		}
		switch r.PathValue("action") {
		case "login":
			authURL, err := provider.StartLogin(r.Context(), request.NewAccount)
			if err != nil {
				writeError(w, 409, err)
				return
			}
			writeJSON(w, 200, map[string]string{"authorization_url": authURL})
			return
		case "select":
			err = provider.Select(r.Context(), request.Profile)
		case "disconnect":
			err = provider.Disconnect(r.Context())
		default:
			writeError(w, 404, errors.New("unknown account action"))
			return
		}
		if err != nil {
			writeError(w, 409, err)
			return
		}
	}
	status, err := provider.Status(r.Context())
	if err != nil {
		writeError(w, 500, err)
		return
	}
	g.syncChatGPTModels(r.Context(), target, status)
	writeJSON(w, 200, status)
}

func (g *Gateway) syncChatGPTModels(ctx context.Context, target *route, status chatgpt.Status) {
	signature := status.Active + ":"
	connected := false
	for _, profile := range status.Profiles {
		if profile.ID == status.Active && profile.Connected && profile.PlanEnabled {
			connected = true
			signature += "connected"
		}
	}
	target.modelMu.Lock()
	changed := signature != target.authProfile
	target.authProfile = signature
	if changed {
		target.cachedModels = nil
	}
	target.modelMu.Unlock()
	if connected && (changed || len(target.modelsFromCache()) == 0) {
		if models, err := g.discoverRouteModels(ctx, target); err == nil {
			target.modelMu.Lock()
			if target.authProfile == signature {
				target.cachedModels = cloneModels(models)
			}
			target.modelMu.Unlock()
		}
	}
}
