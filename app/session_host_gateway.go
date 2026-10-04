package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GatewayRequest forwards management and model discovery to the managed child.
// Studio never opens OAuth credential files or rotates tokens itself.
func (host *SessionHost) GatewayRequest(ctx context.Context, method, path string, body json.RawMessage) (json.RawMessage, error) {
	if host == nil || host.manager == nil {
		return nil, ErrSessionRuntimeUnavailable
	}
	if path != "/v1/models" && !strings.HasPrefix(path, "/v1/providers/") {
		return nil, errors.New("unsupported Gateway management path")
	}
	loaded, err := host.store.Load()
	if err != nil {
		return nil, err
	}
	if _, err := host.ensureProvider(loaded); err != nil {
		return nil, err
	}
	host.providerMu.Lock()
	defer host.providerMu.Unlock()
	endpoint, key := host.manager.Endpoint(), host.manager.APIKey()
	if endpoint == "" {
		return nil, ErrSessionRuntimeUnavailable
	}
	base := strings.TrimSuffix(strings.TrimRight(endpoint, "/"), "/v1")
	request, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return nil, errors.New("gateway management request failed")
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != 200 {
		var payload struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &payload)
		if payload.Error.Message == "" {
			return nil, fmt.Errorf("gateway management HTTP %d", response.StatusCode)
		}
		return nil, errors.New(payload.Error.Message)
	}
	return data, nil
}
