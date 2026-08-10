// Package tools constructs q's MCP tool server.
package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/agentskills"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/tools/builtin"
	"github.com/snowmerak/q/workspace"
)

const (
	ServerName    = "q-tools"
	ServerVersion = "0.1.0"
)

// NewServer builds an MCP server with the root-jailed builtin tool set.
func NewServer(root string) (*mcp.Server, error) {
	loomRuntime, err := newLoomRuntime(root, loom.NewProcessEvaluator(), withSessionRoots(loom.StoreOptions{}, root))
	if err != nil {
		return nil, err
	}
	server, _, _, err := newServer(root, nil, loomRuntime)
	return server, err
}

// NewServerWithArchive builds an MCP server whose read-only archive tools use
// the supplied workspace store. The caller owns the archive lifetime.
func NewServerWithArchive(root string, archive builtin.Archive) (*mcp.Server, error) {
	loomRuntime, err := newLoomRuntime(root, loom.NewProcessEvaluator(), withSessionRoots(loom.StoreOptions{}, root))
	if err != nil {
		return nil, err
	}
	server, _, _, err := newServer(root, archive, loomRuntime)
	return server, err
}

func newServer(root string, archive builtin.Archive, loomRuntime *builtin.LoomRuntime) (*mcp.Server, *builtin.FS, *agentskills.Registry, error) {
	skills, err := agentskills.Discover(root)
	if err != nil {
		return nil, nil, nil, err
	}
	if store, ok := archive.(agentskills.RecordStore); ok {
		if err := skills.SyncRecords(context.Background(), store); err != nil {
			return nil, nil, nil, fmt.Errorf("tools: index Agent Skills: %w", err)
		}
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    ServerName,
		Version: ServerVersion,
	}, nil)
	fs, err := builtin.Register(server, root, builtin.Dependencies{Archive: archive, Loom: loomRuntime, Skills: skills})
	if err != nil {
		return nil, nil, nil, err
	}
	return server, fs, skills, nil
}

func newLoomRuntime(root string, evaluator loom.Evaluator, options loom.StoreOptions) (*builtin.LoomRuntime, error) {
	store, err := loom.OpenWithOptions(root, options)
	if err != nil {
		return nil, err
	}
	return &builtin.LoomRuntime{Store: store, Evaluator: evaluator}, nil
}

// RunStdio serves the builtin tools over MCP stdio until ctx is cancelled or
// the client disconnects. Callers must keep stdout reserved for MCP messages.
func RunStdio(ctx context.Context, root string) error {
	return RunStdioWithLoomOptions(ctx, root, loom.StoreOptions{})
}

func RunStdioWithLoomOptions(ctx context.Context, root string, options loom.StoreOptions) error {
	workspaceLock, err := workspace.AcquireLock(root, "q-mcp")
	if err != nil {
		return err
	}
	defer workspaceLock.Close()

	archive, err := sessionstore.OpenWithOptions(root, sessionstore.OpenOptions{WorkspaceLock: workspaceLock})
	if err != nil {
		return err
	}
	defer archive.Close()
	loomRuntime, err := newLoomRuntime(root, loom.NewProcessEvaluator(), withSessionRoots(options, root))
	if err != nil {
		return err
	}
	server, fs, _, err := newServer(root, archive, loomRuntime)
	if err != nil {
		return err
	}
	defer fs.Close()
	return server.Run(ctx, &mcp.StdioTransport{})
}

func withSessionRoots(options loom.StoreOptions, root string) loom.StoreOptions {
	options.Roots = func(ctx context.Context) ([]loom.Ref, error) {
		return workspace.LoomReferencesAt(ctx, root)
	}
	return options
}
