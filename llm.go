package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	Tools       []Tool        `json:"tools,omitempty"`
	ToolChoice  string        `json:"tool_choice,omitempty"`
}

type ChatChoice struct {
	Message ChatMessage `json:"message"`
}

type ChatResponse struct {
	Choices []ChatChoice `json:"choices"`
}

type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function CallFunctionDetail `json:"function"`
}

type CallFunctionDetail struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func GetToolsSpec() []Tool {
	properties := map[string]any{
		"command": map[string]any{
			"type":        "string",
			"description": "The exact CLI shell command to run on the user's terminal.",
		},
	}
	parameters := map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   []string{"command"},
	}

	return []Tool{
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "run_shell_command",
				Description: "Execute a CLI shell command on the user's local terminal and return the output.",
				Parameters:  parameters,
			},
		},
	}
}

func GenerateCommand(ctx context.Context, cfg *Config, info *SystemInfo, prompt string) (*ChatMessage, error) {
	messages := CreateInitialMessages(info, prompt)
	return GenerateCommandMultiTurn(ctx, cfg, messages)
}

func CreateInitialMessages(info *SystemInfo, prompt string) []ChatMessage {
	var shellGuideline string
	switch strings.ToLower(info.Shell) {
	case "powershell", "pwsh":
		shellGuideline = "The user is using PowerShell. You MUST generate PowerShell cmdlets (e.g., Get-ChildItem, Get-Process, Out-String) or equivalent Windows PowerShell commands. Do NOT generate Unix/Bash commands (e.g., find, grep, ls, ps). Do NOT use '&&' to chain commands; use ';' or pipeline instead."
	case "cmd":
		shellGuideline = "The user is using Windows Command Prompt (cmd.exe). You MUST generate cmd.exe compatible commands (e.g., dir, findstr, tasklist). Do NOT generate Unix/Bash commands."
	default:
		shellGuideline = fmt.Sprintf("The user is using %s. Generate commands compatible with this shell.", info.Shell)
	}

	systemPrompt := fmt.Sprintf(
		"You are an agentic CLI copilot. Based on the user request and environment, you can run shell commands to investigate or perform actions using the provided tool `run_shell_command`. Keep running commands or analysis until you have fully solved the user request. Once solved, present the final answer/result to the user. Do NOT wrap your final response in code blocks unless you want to output code.\n\nUser Environment:\n- OS: %s\n- Shell: %s\n- Username: %s\n- PWD: %s\n- Guide: %s",
		info.OS, info.Shell, info.Username, info.PWD, shellGuideline,
	)

	return []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: prompt},
	}
}

func GenerateCommandMultiTurn(ctx context.Context, cfg *Config, messages []ChatMessage) (*ChatMessage, error) {
	reqBody := ChatRequest{
		Model:       cfg.Model,
		Messages:    messages,
		Temperature: 0.1,
		Tools:       GetToolsSpec(),
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	url := strings.TrimSuffix(cfg.APIEndpoint, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned non-200 status code: %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, err
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("no response choices returned from API")
	}

	assistantMsg := chatResp.Choices[0].Message
	assistantMsg.Content = cleanCommand(assistantMsg.Content)
	return &assistantMsg, nil
}

func cleanCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if strings.HasPrefix(cmd, "```") {
		if firstNewline := strings.Index(cmd, "\n"); firstNewline != -1 {
			cmd = cmd[firstNewline+1:]
		} else {
			cmd = cmd[3:]
		}
		if strings.HasSuffix(cmd, "```") {
			cmd = cmd[:len(cmd)-3]
		}
		cmd = strings.TrimSpace(cmd)
	}
	if strings.HasPrefix(cmd, "`") && strings.HasSuffix(cmd, "`") {
		cmd = cmd[1 : len(cmd)-1]
		return strings.TrimSpace(cmd)
	}
	return cmd
}
