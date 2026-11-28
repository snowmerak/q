package main

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/snowmerak/q/v2/pkg/llm/gateway"
)

var errRuntimeClosed = errors.New("gateway runtime is closed")

type gatewayGeneration struct {
	gateway            *gateway.Gateway
	handler            http.Handler
	requests           sync.WaitGroup
	waitAuthentication func(context.Context) error
}

// gatewayRuntime pins each request to the Gateway generation it starts on.
// Reloads publish a warmed replacement immediately, then wait for requests on
// the retired generation to finish before closing its providers.
type gatewayRuntime struct {
	mu      sync.Mutex
	current *gatewayGeneration
	closed  bool
}

func newGatewayRuntime(ctx context.Context, config gateway.Config) (*gatewayRuntime, error) {
	instance, err := gateway.NewContext(ctx, config)
	if err != nil {
		return nil, err
	}
	return &gatewayRuntime{current: newGatewayGeneration(instance)}, nil
}

func (r *gatewayRuntime) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	r.mu.Lock()
	if r.closed || r.current == nil {
		r.mu.Unlock()
		http.Error(writer, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	generation := r.current
	generation.requests.Add(1)
	r.mu.Unlock()
	defer generation.requests.Done()
	generation.handler.ServeHTTP(writer, request)
}

func (r *gatewayRuntime) Reload(ctx context.Context, config gateway.Config) (bool, error) {
	replacement, err := gateway.NewContext(ctx, config)
	if err != nil {
		return false, err
	}

	replacementGeneration := newGatewayGeneration(replacement)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = replacement.Close()
		return false, errRuntimeClosed
	}
	previous := r.current
	r.current = replacementGeneration
	r.mu.Unlock()

	previous.requests.Wait()
	var authErr error
	if previous.waitAuthentication != nil {
		authErr = previous.waitAuthentication(ctx)
	}
	return true, errors.Join(authErr, previous.close())
}

func (r *gatewayRuntime) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	previous := r.current
	r.current = nil
	r.mu.Unlock()
	if previous == nil {
		return nil
	}
	return previous.close()
}

func newGatewayGeneration(instance *gateway.Gateway) *gatewayGeneration {
	return &gatewayGeneration{gateway: instance, handler: instance.Handler(), waitAuthentication: instance.WaitForAuthentication}
}

func (g *gatewayGeneration) close() error {
	g.requests.Wait()
	if g.gateway == nil {
		return nil
	}
	return g.gateway.Close()
}

var _ http.Handler = (*gatewayRuntime)(nil)
