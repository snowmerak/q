package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	ServiceName     = "q-library"
	ProtocolVersion = 1
	Implementation  = "0.1.0"
)

type Health struct {
	Service         string `json:"service"`
	ProtocolVersion int    `json:"protocol_version"`
	Implementation  string `json:"implementation"`
	StoreID         string `json:"store_id"`
	Generation      string `json:"generation"`
	Ready           bool   `json:"ready"`
}

func (h Health) Compatible() bool {
	return h.Service == ServiceName && h.ProtocolVersion == ProtocolVersion && h.Ready
}

type Client struct {
	endpoint string
	apiKey   string
	http     *http.Client
}

func NewClient(endpoint, apiKey string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = time.Duration(defaultProbeTimeout) * time.Millisecond
	}
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"), apiKey: apiKey,
		http: &http.Client{Timeout: timeout},
	}
}

func (c *Client) Endpoint() string { return c.endpoint }

func (c *Client) Health(ctx context.Context) (Health, error) {
	return c.getHealth(ctx, "/health", false)
}

func (c *Client) Status(ctx context.Context) (Health, error) {
	return c.getHealth(ctx, "/status", true)
}

func (c *Client) getHealth(ctx context.Context, path string, authenticated bool) (Health, error) {
	if c == nil {
		return Health{}, errors.New("library: client is nil")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+path, nil)
	if err != nil {
		return Health{}, err
	}
	if authenticated && c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return Health{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Health{}, fmt.Errorf("library: %s returned HTTP %d", path, response.StatusCode)
	}
	var result Health
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return Health{}, fmt.Errorf("library: decode %s: %w", path, err)
	}
	return result, nil
}

func (c *Client) SearchSkills(ctx context.Context, input SkillSearchRequest) (SkillSearchResponse, error) {
	var output SkillSearchResponse
	if err := c.doJSON(ctx, http.MethodPost, "/skills/search", input, &output); err != nil {
		return SkillSearchResponse{}, err
	}
	return output, nil
}

func (c *Client) GetSkill(ctx context.Context, id, path string) (SkillResource, error) {
	if strings.TrimSpace(path) == "" {
		path = "SKILL.md"
	}
	escapedPath := make([]string, 0)
	for _, part := range strings.Split(strings.ReplaceAll(path, "\\", "/"), "/") {
		escapedPath = append(escapedPath, url.PathEscape(part))
	}
	endpoint := "/skills/" + url.PathEscape(strings.TrimSpace(id)) + "/resources/" + strings.Join(escapedPath, "/")
	var output SkillResource
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &output); err != nil {
		return SkillResource{}, err
	}
	return output, nil
}

func (c *Client) ReloadSkills(ctx context.Context) (SkillReloadResponse, error) {
	var output SkillReloadResponse
	if err := c.doJSON(ctx, http.MethodPost, "/skills/reload", struct{}{}, &output); err != nil {
		return SkillReloadResponse{}, err
	}
	return output, nil
}

func (c *Client) SearchPropositions(ctx context.Context, input PropositionSearchRequest) (PropositionSearchResponse, error) {
	var output PropositionSearchResponse
	if err := c.doJSON(ctx, http.MethodPost, "/propositions/search", input, &output); err != nil {
		return PropositionSearchResponse{}, err
	}
	return output, nil
}

func (c *Client) GetProposition(ctx context.Context, id string) (Proposition, error) {
	var output Proposition
	path := "/propositions/" + url.PathEscape(strings.TrimSpace(id))
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &output); err != nil {
		return Proposition{}, err
	}
	return output, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, input, output any) error {
	if c == nil {
		return errors.New("library: client is nil")
	}
	var body *bytes.Reader
	if input == nil {
		body = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("library: encode %s: %w", path, err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(response.Body).Decode(&envelope)
		if envelope.Error.Message != "" {
			return fmt.Errorf("library: %s returned HTTP %d: %s", path, response.StatusCode, envelope.Error.Message)
		}
		return fmt.Errorf("library: %s returned HTTP %d", path, response.StatusCode)
	}
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("library: decode %s: %w", path, err)
	}
	return nil
}
