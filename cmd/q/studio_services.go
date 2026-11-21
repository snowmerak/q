package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/snowmerak/q/diagnostics"
	"github.com/snowmerak/q/gatewayconfig"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/systemoneconfig"
)

func runStudioGateway(ctx context.Context, directory string, stdout, stderr io.Writer) {
	providers := providerhost.Store{Dir: directory}
	settings := gatewayconfig.Store{Dir: directory}
	runStudioService(ctx, "Gateway", []string{providers.Path(), settings.Path()}, stderr, func(ctx context.Context) error {
		return runGatewayWithStore(ctx, nil, stdout, stderr, providers, settings)
	})
}

func runStudioDecisions(ctx context.Context, directory string, stdout, stderr io.Writer) {
	settings := systemoneconfig.Store{Dir: directory}
	runStudioService(ctx, "Decisions", []string{settings.Path()}, stderr, func(ctx context.Context) error {
		return runSystemOneWithStore(ctx, "decisions", nil, stdout, stderr, settings)
	})
}

// runStudioService runs a standalone server for the lifetime of its owning
// Studio, restarting it when its saved configuration changes.
func runStudioService(ctx context.Context, name string, paths []string, stderr io.Writer, run func(context.Context) error) {
	logger := diagnostics.Logger(ctx)
	event := "studio." + strings.ToLower(name)
	for ctx.Err() == nil {
		logger.Info(event + ".starting")
		before := studioServiceFiles(paths)
		runContext, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- run(runContext) }()

		ticker := time.NewTicker(500 * time.Millisecond)
		var runErr error
		waitForChange := false
	watch:
		for {
			select {
			case <-ctx.Done():
				cancel()
				runErr = <-done
				break watch
			case runErr = <-done:
				waitForChange = true
				break watch
			case <-ticker.C:
				if !sameStudioServiceFiles(before, studioServiceFiles(paths)) {
					logger.Info(event+".restarting", "reason_code", "configuration_changed")
					cancel()
					runErr = <-done
					break watch
				}
			}
		}
		ticker.Stop()
		cancel()
		if ctx.Err() != nil {
			logger.Info(event + ".stopped")
			return
		}
		if runErr != nil && waitForChange {
			logger.Warn(event+".unavailable", "error", runErr)
			_, _ = fmt.Fprintf(stderr, "q studio: standalone %s unavailable: %v\n", name, runErr)
		}
		if waitForChange {
			waitForStudioServiceFiles(ctx, paths, before)
		}
	}
}

type studioServiceFile struct {
	exists   bool
	size     int64
	modified time.Time
}

func studioServiceFiles(paths []string) []studioServiceFile {
	result := make([]studioServiceFile, len(paths))
	for index, path := range paths {
		if info, err := os.Stat(path); err == nil {
			result[index] = studioServiceFile{exists: true, size: info.Size(), modified: info.ModTime()}
		}
	}
	return result
}

func sameStudioServiceFiles(left, right []studioServiceFile) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func waitForStudioServiceFiles(ctx context.Context, paths []string, before []studioServiceFile) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !sameStudioServiceFiles(before, studioServiceFiles(paths)) {
				return
			}
		}
	}
}
