// Package tools constructs q's MCP tool server.
package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/tools/builtin"
)

const (
	ServerName    = "q-tools"
	ServerVersion = "0.1.0"
)

// NewServer builds an MCP server with the root-jailed builtin tool set.
func NewServer(root string) (*mcp.Server, error) {
	server, _, err := newServer(root)
	return server, err
}

func newServer(root string) (*mcp.Server, *builtin.FS, error) {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    ServerName,
		Version: ServerVersion,
	}, nil)
	fs, err := builtin.Register(server, root)
	if err != nil {
		return nil, nil, err
	}
	return server, fs, nil
}

// RunStdio serves the builtin tools over MCP stdio until ctx is cancelled or
// the client disconnects. Callers must keep stdout reserved for MCP messages.
func RunStdio(ctx context.Context, root string) error {
	server, fs, err := newServer(root)
	if err != nil {
		return err
	}
	defer fs.Close()
	return server.Run(ctx, &mcp.StdioTransport{})
}
