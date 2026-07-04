package main

import (
	"fmt"
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


