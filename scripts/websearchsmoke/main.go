// websearchsmoke explicitly invokes Q's real MCP web_search tool. It uses the
// user's configured models and network, and prints the normal Loom receipt.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/tools"
)

func main() {
	input := flag.String("input", "", "UTF-8 JSON file containing web_search arguments")
	output := flag.String("output", "", "Optional file for the JSON Loom receipt")
	tool := flag.String("tool", "web_search", "web_search or web_search_batch")
	flag.Parse()
	if err := run(*input, *output, *tool); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(input, output, tool string) (returnErr error) {
	if tool != "web_search" && tool != "web_search_batch" {
		return fmt.Errorf("-tool must be web_search or web_search_batch")
	}
	if input == "" {
		return fmt.Errorf("-input is required; this command makes real model and web calls")
	}
	body, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	if !json.Valid(body) {
		return fmt.Errorf("input must be JSON")
	}
	root, err := os.MkdirTemp("", "q-websearch-smoke-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(root) }()
	ctx, cancel := context.WithTimeout(context.Background(), 36*time.Minute)
	defer cancel()
	runtime, err := tools.NewRuntime(ctx, root)
	if err != nil {
		return err
	}
	defer func() {
		if err := runtime.Close(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()
	result, err := runtime.Call(ctx, client.ToolCall{ID: "websearch-smoke", Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: tool, Arguments: string(body)}})
	if err != nil {
		return err
	}
	if output != "" {
		if err := os.WriteFile(output, []byte(result.Content+"\n"), 0600); err != nil {
			return err
		}
		if err := exportArtifact(ctx, root, output, result.Content); err != nil {
			return err
		}
	}
	fmt.Println(result.Content)
	if result.IsError {
		return fmt.Errorf("%s returned a tool error", tool)
	}
	return nil
}

// Preserve the complete payload before deleting the temporary Loom store,
// including when the normal receipt contains only a bounded preview.
func exportArtifact(ctx context.Context, root, output, receipt string) error {
	var captured struct {
		Ref string `json:"loom_ref"`
	}
	if err := json.Unmarshal([]byte(receipt), &captured); err != nil {
		return err
	}
	ref, err := loom.ParseRef(captured.Ref)
	if err != nil {
		return err
	}
	store, err := loom.Open(root)
	if err != nil {
		return err
	}
	body, err := store.ReadAll(ctx, ref, 8<<20)
	if err != nil {
		return err
	}
	return os.WriteFile(output+".artifact.json", body, 0600)
}
