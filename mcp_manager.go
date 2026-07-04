package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

type MCPManager struct {
	clients map[string]*MCPClient
	mu      sync.RWMutex
}

var GlobalMCPManager = &MCPManager{
	clients: make(map[string]*MCPClient),
}

// InitMCPManager starts all configured MCP clients.
func InitMCPManager(cfg *Config) {
	GlobalMCPManager.mu.Lock()
	defer GlobalMCPManager.mu.Unlock()

	for name, mcpCfg := range cfg.MCPServers {
		client := NewMCPClient(name, mcpCfg)
		PrintInfo(fmt.Sprintf("Starting MCP server '%s' (%s)...", name, mcpCfg.Command))
		if err := client.Start(); err != nil {
			PrintError(fmt.Sprintf("Failed to start MCP server '%s': %v", name, err))
			continue
		}
		GlobalMCPManager.clients[name] = client
		PrintSuccess(fmt.Sprintf("MCP server '%s' started successfully.", name))
	}
}

// StopMCPManager stops all running MCP clients.
func StopMCPManager() {
	GlobalMCPManager.mu.Lock()
	defer GlobalMCPManager.mu.Unlock()

	for name, client := range GlobalMCPManager.clients {
		PrintInfo(fmt.Sprintf("Stopping MCP server '%s'...", name))
		_ = client.Stop()
		delete(GlobalMCPManager.clients, name)
	}
}

// GetMCPTools aggregates tools from all running MCP clients and formats them as OpenAI tools.
func GetMCPTools() []Tool {
	GlobalMCPManager.mu.RLock()
	defer GlobalMCPManager.mu.RUnlock()

	var tools []Tool
	for serverName, client := range GlobalMCPManager.clients {
		mcpTools, err := client.ListTools()
		if err != nil {
			PrintError(fmt.Sprintf("Failed to list tools for MCP server '%s': %v", serverName, err))
			continue
		}

		for _, mt := range mcpTools {
			prefixedName := fmt.Sprintf("%s__%s", serverName, mt.Name)

			var params map[string]any
			if len(mt.InputSchema) > 0 {
				_ = json.Unmarshal(mt.InputSchema, &params)
			}

			tools = append(tools, Tool{
				Type: "function",
				Function: ToolFunction{
					Name:        prefixedName,
					Description: fmt.Sprintf("[%s] %s", serverName, mt.Description),
					Parameters:  params,
				},
			})
		}
	}

	return tools
}

// CallMCPTool routes a tool call to the appropriate MCP client.
func CallMCPTool(prefixedName string, arguments any) (string, error) {
	parts := strings.SplitN(prefixedName, "__", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid MCP tool name format: %s", prefixedName)
	}

	serverName := parts[0]
	toolName := parts[1]

	GlobalMCPManager.mu.RLock()
	client, ok := GlobalMCPManager.clients[serverName]
	GlobalMCPManager.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("MCP server '%s' is not running", serverName)
	}

	return client.CallTool(toolName, arguments)
}
