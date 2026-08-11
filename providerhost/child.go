package providerhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
)

const ChildCommand = "__gateway-child"

type ReadyMessage struct {
	Event   string `json:"event"`
	BaseURL string `json:"base_url"`
}

// RunChild serves one immutable Gateway configuration until ctx is cancelled.
// The first stdout line is a machine-readable readiness handshake.
func RunChild(ctx context.Context, configPath, apiKey string, ready func(ReadyMessage) error) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("providerhost: listen: %w", err)
	}
	defer listener.Close()

	value, err := gateway.LoadConfig(configPath)
	if err != nil {
		return err
	}
	instance, err := gateway.NewContext(ctx, value)
	if err != nil {
		return err
	}
	defer instance.Close()

	server := &http.Server{
		Handler:           AuthenticatedHandler(apiKey, instance.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()

	message := ReadyMessage{
		Event:   "ready",
		BaseURL: "http://" + listener.Addr().String(),
	}
	if err := ready(message); err != nil {
		_ = server.Close()
		<-serveDone
		return fmt.Errorf("providerhost: write readiness message: %w", err)
	}

	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
		err := <-serveDone
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-serveDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func EncodeReady(encoder *json.Encoder) func(ReadyMessage) error {
	return func(message ReadyMessage) error { return encoder.Encode(message) }
}
