package app

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/internal/librarymcp"
	"github.com/snowmerak/q/internal/workitemtools"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

const acpLibraryInstructionsVersion = 6

func acpLibraryServers(configDir string, configured ...workitemtools.Binding) ([]acp.McpServer, error) {
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
	if len(configured) > 0 {
		binding := configured[0]
		servers[0].Stdio.Args = append(servers[0].Stdio.Args, "--workspace-id", binding.WorkspaceID, "--session-id", binding.SessionID)
		if binding.Manager {
			servers[0].Stdio.Args = append(servers[0].Stdio.Args, "--manager")
		}
		if binding.EmbeddingConnection != "" {
			servers[0].Stdio.Args = append(servers[0].Stdio.Args, "--embedding-connection", binding.EmbeddingConnection)
		}
	}
	return servers, nil
}

func (r *acpRemoteClient) libraryPrompt(content, restored string) (string, bool) {
	include := len(r.mcpServers) > 0 && !r.libraryInstructionsSent
	prefix := restored
	if include {
		prefix = strings.TrimSpace(prefix + "\n\n" + librarymcp.Instructions)
		if r.libraryRoleInstructions != "" {
			prefix += "\n\n" + r.libraryRoleInstructions
		}
	}
	if prefix != "" {
		content = prefix + "\n\nCurrent request:\n" + content
	}
	return content, include
}
