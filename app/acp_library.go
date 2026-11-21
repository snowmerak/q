package app

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/internal/librarymcp"
	"github.com/snowmerak/q/internal/workitemtools"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

const acpLibraryInstructionsVersion = 9

func acpLibraryServers(configDir string, binding *workitemtools.Binding) ([]acp.McpServer, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	dir, err := filepath.Abs(configDir)
	if err != nil {
		return nil, err
	}
	servers := []acp.McpServer{{Stdio: &acp.McpServerStdio{
		Name: "q-library", Command: executable,
		Args: []string{"library", "mcp", "--config-dir", dir}, Env: []acp.EnvVariable{},
	}}}
	server := servers[0].Stdio
	if binding == nil {
		server.Args = append(server.Args, "--skills-only")
		return servers, nil
	}
	server.Args = append(server.Args, "--workspace-id", binding.WorkspaceID, "--session-id", binding.SessionID)
	if binding.Manager {
		server.Args = append(server.Args, "--manager")
	}
	if binding.EmbeddingConnection != "" {
		server.Args = append(server.Args, "--embedding-connection", binding.EmbeddingConnection)
	}
	return servers, nil
}

func (r *acpRemoteClient) libraryPrompt(content, restored string) (string, bool) {
	include := len(r.mcpServers) > 0 && !r.libraryInstructionsSent
	prefix := restored
	if include {
		instructions := r.libraryInstructions
		if instructions == "" {
			instructions = librarymcp.InstructionsFor(&workitemtools.Binding{})
		}
		prefix = strings.TrimSpace(prefix + "\n\n" + instructions)
		if r.libraryRoleInstructions != "" {
			prefix += "\n\n" + r.libraryRoleInstructions
		}
	}
	if prefix != "" {
		content = prefix + "\n\nCurrent request:\n" + content
	}
	return content, include
}
