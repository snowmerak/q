package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/snowmerak/q/gatewayconfig"
	"github.com/snowmerak/q/providerhost"
)

// runStudioGateway keeps the standalone Gateway alongside Studio. It uses the
// same server path as `q gateway start` and restarts after saved settings change.
func runStudioGateway(ctx context.Context, directory string, stdout, stderr io.Writer) {
	providers := providerhost.Store{Dir: directory}
	settings := gatewayconfig.Store{Dir: directory}
	paths := []string{providers.Path(), settings.Path()}
	for ctx.Err() == nil {
		before := gatewayFiles(paths)
		runContext, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- runGatewayWithStore(runContext, nil, stdout, stderr, providers, settings)
		}()

		ticker := time.NewTicker(500 * time.Millisecond)
		var runErr error
		waitForChange := false
	watch:
		for {
			select {
			case <-ctx.Done():
				cancel()
				<-done
				break watch
			case runErr = <-done:
				waitForChange = true
				break watch
			case <-ticker.C:
				if !sameGatewayFiles(before, gatewayFiles(paths)) {
					cancel()
					runErr = <-done
					break watch
				}
			}
		}
		ticker.Stop()
		cancel()
		if ctx.Err() != nil {
			return
		}
		if runErr != nil && waitForChange {
			_, _ = fmt.Fprintf(stderr, "q studio: standalone Gateway unavailable: %v\n", runErr)
		}
		if waitForChange {
			waitForGatewayFiles(ctx, paths, before)
		}
	}
}

type gatewayFile struct {
	exists   bool
	size     int64
	modified time.Time
}

func gatewayFiles(paths []string) []gatewayFile {
	result := make([]gatewayFile, len(paths))
	for index, path := range paths {
		if info, err := os.Stat(path); err == nil {
			result[index] = gatewayFile{exists: true, size: info.Size(), modified: info.ModTime()}
		}
	}
	return result
}

func sameGatewayFiles(left, right []gatewayFile) bool {
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

func waitForGatewayFiles(ctx context.Context, paths []string, before []gatewayFile) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !sameGatewayFiles(before, gatewayFiles(paths)) {
				return
			}
		}
	}
}
