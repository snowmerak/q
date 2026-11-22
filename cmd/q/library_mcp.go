package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/internal/embeddingbridge"
	"github.com/snowmerak/q/internal/librarymcp"
	"github.com/snowmerak/q/internal/workitemtools"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/workitems"
)

func runLibraryMCP(ctx context.Context, args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("q library mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("config-dir", "", "Q configuration directory containing library.json")
	skillsOnly := flags.Bool("skills-only", false, "Expose only the global Agent Skills read tools")
	workspaceID := flags.String("workspace-id", "", "Host-selected Studio project ID")
	sessionID := flags.String("session-id", "", "Host-selected Q session ID")
	manager := flags.Bool("manager", false, "Host-selected project manager binding")
	reviewAttempt := flags.String("review-attempt", "", "Host-selected attempt for this review turn")
	reviewOperation := flags.String("review-operation", "", "Host-selected operation for this review turn")
	connection := flags.String("embedding-connection", "", "Host-owned Gateway connection for semantic queries")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return flag.ErrHelp
	}
	if *dir == "" {
		store, err := config.DefaultStore()
		if err != nil {
			return err
		}
		*dir = store.Dir
	}
	value, err := (library.ConfigStore{Dir: *dir}).LoadOrDefault()
	if err != nil {
		return err
	}
	// The application host owns Library. This bridge stays available when its
	// HTTP service is temporarily down; individual tool calls report errors.
	client := library.NewClient(value.Endpoint(), "", 0)
	if *skillsOnly {
		return librarymcp.NewServer(client, nil).Run(ctx, &mcp.StdioTransport{})
	}
	if *connection != "" {
		store := config.Store{Dir: *dir}
		client.SetWorkEmbeddingResolver(func(context.Context) (library.Embedder, string, int, error) {
			value, err := store.Load()
			if err != nil {
				return nil, "", 0, err
			}
			if value.Embedding.Model == "" {
				return nil, "", 0, nil
			}
			return &embeddingbridge.Embedder{Path: *connection, Store: store, Model: value.Embedding.Model, Dimensions: value.Embedding.Dimensions}, value.Embedding.Model, value.Embedding.Dimensions, nil
		})
	}
	binding := &workitemtools.Binding{WorkspaceID: *workspaceID, SessionID: *sessionID, Manager: *manager}
	if raw := os.Getenv("Q_LIBRARY_TICKET_WORKER"); raw != "" {
		binding.Worker = new(workitems.WorkerBinding)
		if err := json.Unmarshal([]byte(raw), binding.Worker); err != nil {
			return err
		}
	}
	if *reviewAttempt != "" || *reviewOperation != "" {
		binding.Review = workitemtools.ReviewAssignment{AttemptID: *reviewAttempt, OperationID: *reviewOperation}
		if !binding.Review.Active() || *workspaceID == "" || *sessionID == "" || (!binding.Manager && binding.Worker == nil) {
			return errors.New("assigned review requires an attempt, operation, project, and reviewer binding")
		}
	}
	return librarymcp.NewServer(client, binding).Run(ctx, &mcp.StdioTransport{})
}
