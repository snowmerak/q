package app

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/internal/librarymcp"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

const acpLibraryInstructionsVersion = 2

func acpLibraryServers(configDir string) ([]acp.McpServer, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	dir, err := filepath.Abs(configDir)
	if err != nil {
		return nil, err
	}
	return []acp.McpServer{{Stdio: &acp.McpServerStdio{
		Name: "q-library", Command: executable,
		Args: []string{"library", "mcp", "--config-dir", dir}, Env: []acp.EnvVariable{},
	}}}, nil
}

func (r *acpRemoteClient) libraryPrompt(content, restored string) (string, bool) {
	include := len(r.mcpServers) > 0 && !r.libraryInstructionsSent
	prefix := restored
	if include {
		prefix = strings.TrimSpace(prefix + "\n\n" + librarymcp.Instructions)
	}
	if prefix != "" {
		content = prefix + "\n\nCurrent request:\n" + content
	}
	return content, include
}
