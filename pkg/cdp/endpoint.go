package qdriver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func browserEndpoint(ctx context.Context, endpoint *url.URL) (string, error) {
	if endpoint.Scheme == "ws" || endpoint.Scheme == "wss" {
		if !strings.HasPrefix(endpoint.Path, "/devtools/browser/") || strings.TrimPrefix(endpoint.Path, "/devtools/browser/") == "" {
			return "", errors.New("use a browser WebSocket URL, not a page target or incomplete address")
		}
		return endpoint.String(), nil
	}
	if endpoint.Path == "" || endpoint.Path == "/" {
		endpoint.Path = "/json/version"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", err
	}
	// Do not inherit a process-global client's cookies, transports, or timeout.
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("CDP discovery returned HTTP %d", response.StatusCode)
	}
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&version); err != nil {
		return "", fmt.Errorf("decode CDP discovery: %w", err)
	}
	u, err := url.Parse(version.WebSocketDebuggerURL)
	if err != nil || u.Host == "" || (u.Scheme != "ws" && u.Scheme != "wss") {
		return "", errors.New("CDP discovery did not return a browser WebSocket URL")
	}
	return browserEndpoint(ctx, u)
}
