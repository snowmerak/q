package studio

import (
	"context"
	"github.com/snowmerak/q/config"
	"net/http"
)

func newHandler(store config.Store) (http.Handler, error) {
	return newHandlerWithRunner(store, nil)
}

func newHandlerWithRunner(store config.Store, runner sessionRunner) (http.Handler, error) {
	handler, _, _, err := newHandlerRuntime(context.Background(), store, runner)
	return handler, err
}
