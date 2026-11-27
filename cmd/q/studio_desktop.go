package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/snowmerak/q/internal/studioprocess"
)

// runStudioDesktop is a private, versioned stdio protocol, not a user-facing
// CLI mode. Diagnostics never share stdout with the readiness record.
func runStudioDesktop(parent context.Context, input io.Reader, output, diagnostics io.Writer) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	reader := bufio.NewReaderSize(input, 4096)
	started := make(chan error, 1)
	go func() {
		line, err := reader.ReadSlice('\n')
		if err == nil {
			var request struct {
				Protocol int `json:"protocol"`
			}
			err = json.Unmarshal(line, &request)
			if err == nil && request.Protocol != studioprocess.Protocol {
				err = errors.New("incompatible Q Studio startup protocol")
			}
		}
		started <- err
		if err == nil {
			_, _ = io.Copy(io.Discard, reader)
		}
		cancel()
	}()
	select {
	case err := <-started:
		if err != nil {
			return err
		}
	case <-time.After(15 * time.Second):
		return errors.New("q Studio startup handshake timed out")
	case <-ctx.Done():
		return ctx.Err()
	}
	return serveStudio(ctx, studioCommandOptions{host: "127.0.0.1", noOpen: true}, diagnostics, diagnostics, nil, "/", &studioDesktopOwner{
		ready: func(endpoint studioprocess.Endpoint) error { return json.NewEncoder(output).Encode(endpoint) },
	})
}
