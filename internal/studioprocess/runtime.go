// Package studioprocess owns discovery and local access to a user-level Studio.
// The OS lock, rather than a PID or the existence of the descriptor, owns it.
package studioprocess

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/worklock"
)

const Protocol = 1
const ControlPath = "/__q_studio/"

// Endpoint is the private descriptor and desktop startup protocol. Token must
// only travel over owner-readable files or the inherited startup pipe.
type Endpoint struct {
	Protocol int    `json:"protocol"`
	URL      string `json:"url"`
	Token    string `json:"token"`
	Instance string `json:"instance"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Secure   bool   `json:"secure"`
	Owned    bool   `json:"owned"`
}

func Secret() string { return rand.Text() + rand.Text() }

func Acquire(directory string) (*worklock.Lock, error) {
	private := filepath.Join(directory, "studio")
	if err := os.MkdirAll(private, 0o700); err != nil {
		return nil, err
	}
	if err := secureDirectory(private); err != nil {
		return nil, err
	}
	return worklock.AcquireFile(directory, "studio/runtime.lock", "q studio")
}

func descriptorPath(directory string) string {
	return filepath.Join(directory, "studio", "runtime.json")
}

func Publish(directory string, endpoint Endpoint) error {
	body, err := json.Marshal(endpoint)
	if err != nil {
		return err
	}
	return fsreplace.WriteFile(descriptorPath(directory), body, 0o600)
}

// Remove is called while the owner's OS lock is still held.
func Remove(directory string) { _ = os.Remove(descriptorPath(directory)) }

func Discover(ctx context.Context, directory string) (Endpoint, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		endpoint, err := read(directory)
		if err == nil {
			err = endpoint.Check(ctx)
		}
		if err == nil {
			endpoint.Owned = false
			return endpoint, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return Endpoint{}, fmt.Errorf("existing Studio unavailable: %w", errors.Join(ctx.Err(), lastErr))
		case <-ticker.C:
		}
	}
}

func read(directory string) (Endpoint, error) {
	file, err := os.Open(descriptorPath(directory))
	if err != nil {
		return Endpoint{}, err
	}
	defer func() { _ = file.Close() }()
	var endpoint Endpoint
	if err := json.NewDecoder(io.LimitReader(file, 8192)).Decode(&endpoint); err != nil {
		return Endpoint{}, err
	}
	return endpoint, endpoint.Validate()
}

func (endpoint Endpoint) Validate() error {
	u, err := url.Parse(endpoint.URL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" ||
		!net.ParseIP(u.Hostname()).IsLoopback() || u.Port() == "" || endpoint.Protocol != Protocol || len(endpoint.Token) < 32 || endpoint.Instance == "" {
		return errors.New("incompatible Studio runtime descriptor")
	}
	return nil
}

func (endpoint Endpoint) Request(ctx context.Context, method, path string) (*http.Response, error) {
	if err := endpoint.Validate(); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.URL+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	return client.Do(request)
}

func (endpoint Endpoint) Check(ctx context.Context) error {
	response, err := endpoint.Request(ctx, http.MethodGet, ControlPath+"status")
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	var status struct {
		Protocol int    `json:"protocol"`
		Instance string `json:"instance"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&status) != nil || status.Protocol != Protocol || status.Instance != endpoint.Instance {
		return errors.New("studio owner verification failed")
	}
	return nil
}

func (endpoint Endpoint) BrowserURL(ctx context.Context, path string) (string, error) {
	if path == "" {
		path = "/"
	}
	if !endpoint.Secure {
		return endpoint.URL + path, nil
	}
	response, err := endpoint.Request(ctx, http.MethodPost, ControlPath+"launch?path="+url.QueryEscape(path))
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	var launch struct {
		Path string `json:"path"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&launch) != nil || !strings.HasPrefix(launch.Path, ControlPath+"enter/") {
		return "", errors.New("studio browser session could not be created")
	}
	return endpoint.URL + launch.Path, nil
}
