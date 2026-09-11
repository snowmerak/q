package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/usagelog"
)

var openUsageBrowser = openBrowserURL

func runUsageCommand(ctx context.Context, output, errorOutput io.Writer) error {
	store, err := config.DefaultStore()
	if err != nil {
		return err
	}
	return runUsageCommandWithStore(ctx, store, output, errorOutput, openUsageBrowser)
}

func runUsageCommandWithStore(ctx context.Context, store config.Store, output, errorOutput io.Writer, open func(string) error) error {
	usageRuntime, err := usagelog.Ensure(ctx, store.Dir)
	if err != nil {
		return err
	}
	url := usageRuntime.DashboardURL()
	if output != nil {
		_, _ = fmt.Fprintln(output, url)
	}
	if err := open(url); err != nil && errorOutput != nil {
		_, _ = fmt.Fprintf(errorOutput, "q usage: could not open browser: %v\n", err)
	}
	if !usageRuntime.IsLeader() {
		if err := usageRuntime.Close(); err != nil {
			return err
		}
		return usagelog.Run(ctx, store.Dir, io.Discard)
	}
	select {
	case <-ctx.Done():
		return usageRuntime.Close()
	case <-usageRuntime.Done():
		return usageRuntime.Close()
	}
}

func openBrowserURL(url string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		command = exec.Command("open", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
