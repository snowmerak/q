package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	APIEndpoint string `json:"api_endpoint"`
	Model       string `json:"model"`
}

const (
	defaultEndpoint = "http://localhost:1234/v1"
	defaultModel    = "lfm2.5-230m"
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

	return &cfg, nil
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
		APIEndpoint: endpointInput,
		Model:       modelInput,
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
