package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	APIEndpoint      string `json:"api_endpoint"`
	Model            string `json:"model"`
	MaxContextTokens int    `json:"max_context_tokens"`
	LastSession      string `json:"last_session"`
}

const (
	defaultEndpoint      = "http://localhost:1234/v1"
	defaultModel         = "lfm2.5-230m"
	defaultContextTokens = 4096
)

func getConfigFilePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	appDir := filepath.Join(configDir, "q")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		return "", err
	}
	return filepath.Join(appDir, "config.json"), nil
}

func LoadConfig() (*Config, error) {
	filePath, err := getConfigFilePath()
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return createConfigPrompt(filePath)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	changed := false
	if cfg.MaxContextTokens == 0 {
		cfg.MaxContextTokens = defaultContextTokens
		changed = true
	}

	if changed {
		_ = SaveConfig(&cfg)
	}

	return &cfg, nil
}

func SaveConfig(cfg *Config) error {
	filePath, err := getConfigFilePath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, data, 0644)
}

func createConfigPrompt(filePath string) (*Config, error) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Printf("Enter LLM API Endpoint [%s]: ", defaultEndpoint)
	endpointInput, _ := reader.ReadString('\n')
	endpointInput = strings.TrimSpace(endpointInput)
	if endpointInput == "" {
		endpointInput = defaultEndpoint
	}

	fmt.Printf("Enter LLM Model [%s]: ", defaultModel)
	modelInput, _ := reader.ReadString('\n')
	modelInput = strings.TrimSpace(modelInput)
	if modelInput == "" {
		modelInput = defaultModel
	}

	cfg := &Config{
		APIEndpoint:      endpointInput,
		Model:            modelInput,
		MaxContextTokens: defaultContextTokens,
		LastSession:      "",
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) GetConfigValue(key string) (string, error) {
	switch strings.ToLower(key) {
	case "api_endpoint", "endpoint":
		return c.APIEndpoint, nil
	case "model":
		return c.Model, nil
	case "max_context_tokens", "tokens":
		return strconv.Itoa(c.MaxContextTokens), nil
	case "last_session":
		return c.LastSession, nil
	default:
		return "", fmt.Errorf("unknown config key '%s'", key)
	}
}

func (c *Config) SetConfigValue(key, val string) error {
	switch strings.ToLower(key) {
	case "api_endpoint", "endpoint":
		c.APIEndpoint = val
		return nil
	case "model":
		c.Model = val
		return nil
	case "max_context_tokens", "tokens":
		v, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid integer value for %s: %w", key, err)
		}
		c.MaxContextTokens = v
		return nil
	case "last_session":
		c.LastSession = val
		return nil
	default:
		return fmt.Errorf("unknown config key '%s'", key)
	}
}
