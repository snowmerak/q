package studio

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	llmprovider "github.com/snowmerak/llm-provider"
)

type gatewayManagementRuntime interface {
	GatewayRequest(context.Context, string, string, json.RawMessage) (json.RawMessage, error)
}

func (service *settingsService) serveChatGPT(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		writeAPIError(w, 403, errors.New("ChatGPT account management requires local Studio access"))
		return
	}
	localHost := r.Host
	if name, _, err := net.SplitHostPort(localHost); err == nil {
		localHost = name
	}
	if localHost != "localhost" {
		address := net.ParseIP(strings.Trim(localHost, "[]"))
		if address == nil || !address.IsLoopback() {
			writeAPIError(w, 403, errors.New("invalid local management host"))
			return
		}
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host != r.Host || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			writeAPIError(w, 403, errors.New("cross-origin account management is not allowed"))
			return
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeAPIError(w, 403, errors.New("cross-site account management is not allowed"))
		return
	}
	runtime, ok := service.runtime.(gatewayManagementRuntime)
	if !ok {
		writeAPIError(w, 503, errors.New("ChatGPT connection requires the running Q Gateway"))
		return
	}
	path := "/v1/providers/" + url.PathEscape(r.PathValue("provider")) + "/chatgpt"
	var body json.RawMessage
	if r.Method == http.MethodPost {
		var action struct {
			NewAccount bool   `json:"new_account"`
			Profile    string `json:"profile"`
		}
		if err := decodeSettingsRequest(w, r, &action); err != nil {
			writeAPIError(w, 400, err)
			return
		}
		body, _ = json.Marshal(action)
		path += "/" + url.PathEscape(r.PathValue("action"))
	}
	data, err := runtime.GatewayRequest(r.Context(), r.Method, path, body)
	if err != nil {
		writeAPIError(w, 502, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func managedModels(ctx context.Context, runtime gatewayManagementRuntime) ([]llmprovider.Model, error) {
	data, err := runtime.GatewayRequest(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Data []llmprovider.Model `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result.Data, nil
}
