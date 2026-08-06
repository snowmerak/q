// Package tools constructs q's MCP tool server.
package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/tools/builtin"
)

const (
	ServerName    = "q-tools"
	ServerVersion = "0.1.0"
)

// NewServer builds an MCP server with the root-jailed builtin tool set.
func NewServer(root string) (*mcp.Server, error) {
	loomRuntime, err := newLoomRuntime(root, loom.NewProcessEvaluator())
	if err != nil {
		return nil, err
	}
	server, _, err := newServer(root, nil, loomRuntime)
	return server, err
}

// NewServerWithArchive builds an MCP server whose read-only archive tools use
// the supplied workspace store. The caller owns the archive lifetime.
func NewServerWithArchive(root string, archive builtin.Archive) (*mcp.Server, error) {
	loomRuntime, err := newLoomRuntime(root, loom.NewProcessEvaluator())
	if err != nil {
		return nil, err
	}
	server, _, err := newServer(root, archive, loomRuntime)
	return server, err
}

func newServer(root string, archive builtin.Archive, loomRuntime *builtin.LoomRuntime) (*mcp.Server, *builtin.FS, error) {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    ServerName,
		Version: ServerVersion,
	}, nil)
	fs, err := builtin.Register(server, root, builtin.Dependencies{Archive: archive, Loom: loomRuntime})
	if err != nil {
		return nil, nil, err
	}
	return server, fs, nil
}

func newLoomRuntime(root string, evaluator loom.Evaluator) (*builtin.LoomRuntime, error) {
	store, err := loom.Open(root)
	if err != nil {
		return nil, err
	}
	return &builtin.LoomRuntime{Store: store, Evaluator: evaluator}, nil
}

// RunStdio serves the builtin tools over MCP stdio until ctx is cancelled or
// the client disconnects. Callers must keep stdout reserved for MCP messages.
func RunStdio(ctx context.Context, root string) error {
	archive, err := sessionstore.Open(root)
	if err != nil {
		return err
	}
	defer archive.Close()
	loomRuntime, err := newLoomRuntime(root, loom.NewProcessEvaluator())
	if err != nil {
		return err
	}
	server, fs, err := newServer(root, archive, loomRuntime)
	if err != nil {
		return err
	}
	defer fs.Close()
	return server.Run(ctx, &mcp.StdioTransport{})
}
