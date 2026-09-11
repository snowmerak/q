package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/commitagent"
	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
	"github.com/snowmerak/q/loom"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/workspacememory"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if len(os.Args) > 1 && os.Args[1] == loom.ChildCommand {
		if err := loom.RunChild(ctx, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == providerhost.ChildCommand {
		if err := runGatewayChild(ctx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "acp" {
		if err := runACPCommand(ctx, os.Args[2:], os.Stdin, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "sprint" {
		if err := runSprintCommand(ctx, os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "diagnose" {
		if err := runDiagnoseCommand(ctx, os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "review" {
		if err := runReviewCommand(ctx, os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 {
		runStandalone := standaloneUICommand(os.Args[1])
		if runStandalone != nil {
			if len(os.Args) != 2 {
				fmt.Fprintf(os.Stderr, "usage: q %s\n", os.Args[1])
				os.Exit(2)
			}
			if err := runStandalone(ctx); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "gateway" {
		mode, serviceArgs, ok := parseServiceCommand(os.Args[2:])
		if !ok {
			fmt.Fprintln(os.Stderr, "usage: q gateway | q gateway start [--host <ip>] [--port <port>]")
			os.Exit(2)
		}
		if mode == serviceCommandConfigure {
			if err := app.RunGatewayConfigDefault(ctx); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
		if err := runGatewayCommand(ctx, serviceArgs, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "library" {
		mode, serviceArgs, ok := parseServiceCommand(os.Args[2:])
		if !ok || (mode == serviceCommandStart && len(serviceArgs) != 0) {
			fmt.Fprintln(os.Stderr, "usage: q library | q library start")
			os.Exit(2)
		}
		if mode == serviceCommandConfigure {
			if err := app.RunLibraryConfigDefault(ctx); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		}
		store, err := config.DefaultStore()
		if err == nil {
			err = qlibrary.Run(ctx, store.Dir, os.Stdout)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "memory" {
		if len(os.Args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: q memory")
			os.Exit(2)
		}
		store, err := config.DefaultStore()
		if err == nil {
			err = workspacememory.Run(ctx, store.Dir, os.Stdout)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "usage" {
		if len(os.Args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: q usage")
			os.Exit(2)
		}
		if err := runUsageCommand(ctx, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "commit" {
		if len(os.Args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: q commit")
			os.Exit(2)
		}
		directory, err := os.Getwd()
		if err == nil {
			_, err = commitagent.RunDefault(ctx, directory, os.Stdout)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := app.RunDefault(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type serviceCommandMode uint8

const (
	serviceCommandConfigure serviceCommandMode = iota
	serviceCommandStart
)

func parseServiceCommand(args []string) (serviceCommandMode, []string, bool) {
	if len(args) == 0 {
		return serviceCommandConfigure, nil, true
	}
	if args[0] != "start" {
		return 0, nil, false
	}
	return serviceCommandStart, args[1:], true
}

func standaloneUICommand(name string) func(context.Context) error {
	switch name {
	case "model":
		return app.RunModelDefault
	case "skills":
		return app.RunSkillsDefault
	case "ignore":
		return app.RunIgnoreDefault
	case "lsp":
		return app.RunLSPDefault
	case "mcp":
		return app.RunMCPDefault
	case "agents":
		return app.RunAgentsDefault
	case "help":
		return app.RunHelp
	default:
		return nil
	}
}

func runGatewayChild(parent context.Context, args []string) error {
	if len(args) != 2 || args[0] != "--config" || args[1] == "" {
		return fmt.Errorf("usage: q %s --config <path>", providerhost.ChildCommand)
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	apiKey := os.Getenv(providerhost.ChildAPIKeyEnv)
	_ = os.Unsetenv(providerhost.ChildAPIKeyEnv)
	if apiKey == "" {
		return fmt.Errorf("Gateway child API key is missing")
	}
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		cancel()
	}()
	return providerhost.RunChild(ctx, args[1], apiKey, providerhost.EncodeReady(json.NewEncoder(os.Stdout)))
}
