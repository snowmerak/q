package studio

import (
	"context"
	"net"
	"net/http"
	"os"
	"testing"

	"github.com/snowmerak/q/config"
	qlibrary "github.com/snowmerak/q/library"
)

func newHandler(store config.Store) (http.Handler, error) {
	return newHandlerWithRunner(store, nil)
}

func newHandlerWithRunner(store config.Store, runner sessionRunner) (http.Handler, error) {
	if err := isolateStudioLibrary(store.Dir); err != nil {
		return nil, err
	}
	handler, _, _, err := newHandlerRuntime(context.Background(), store, runner)
	return handler, err
}

func isolateStudioLibrary(dir string) error {
	store := qlibrary.ConfigStore{Dir: dir}
	if _, err := os.Stat(store.Path()); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return err
	}
	return store.Save(qlibrary.Config{Host: "127.0.0.1", Port: port})
}

func studioCatalogTestDirectory(t *testing.T, dir string) string {
	t.Helper()
	if err := isolateStudioLibrary(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}
