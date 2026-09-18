package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/workspace"
	"github.com/snowmerak/q/workspacememory"
)

func TestStartupKeepsToolsWhenArchiveOpenFails(t *testing.T) {
	root := t.TempDir()
	settingsDir := t.TempDir()
	health := workspacememory.Health{
		Service: workspacememory.ServiceName, ProtocolVersion: workspacememory.ProtocolVersion, Ready: true,
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/health":
			_ = json.NewEncoder(writer).Encode(health)
		case "/v1/status":
			_ = json.NewEncoder(writer).Encode(workspacememory.Status{Health: health})
		case "/v1/workspaces/open":
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"code":"invalid_request","message":"archive open failed"}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil {
		t.Fatal(err)
	}
	if err := (workspacememory.ConfigStore{Dir: settingsDir}).Save(workspacememory.Config{
		Host: "127.0.0.1", Port: port,
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := providerhost.NewManager(t.Context(), providerhost.Store{Dir: settingsDir})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	request := startupRequest{
		ctx: t.Context(), memoryCtx: t.Context(), store: config.Store{Dir: settingsDir},
		workspaceStore: workspace.Store{Root: root}, loaded: config.Default(),
		configErr: config.ErrNotFound, manager: manager, lifecycle: newStartupLifecycle(), providerReady: true,
	}
	result := request.run(nil)
	defer request.lifecycle.closeResources()
	if result.err != nil || result.archiveErr == nil || !strings.Contains(result.archiveErr.Error(), "archive open failed") {
		t.Fatalf("startup errors = (%v, %v)", result.err, result.archiveErr)
	}
	if result.tools == nil || result.archive != nil || result.archiveSearch != nil {
		t.Fatalf("startup resources = tools %p, archive %p, search %p", result.tools, result.archive, result.archiveSearch)
	}
	if len(result.tools.Tools()) == 0 {
		t.Fatal("archive failure left the builtin tool runtime empty")
	}
}

func TestRunFromHomeFailsBeforeWorkspaceStartup(t *testing.T) {
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	if err := os.Chdir(home); err != nil {
		t.Fatal(err)
	}
	err = Run(t.Context(), config.Store{Dir: filepath.Join(home, ".q")})
	if err == nil || !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("Run() from home error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".q")); !os.IsNotExist(err) {
		t.Fatalf("home workspace metadata was created: %v", err)
	}
}

func TestInitialModelLoadWait(t *testing.T) {
	if initialModelLoadWait != 1500*time.Millisecond {
		t.Fatalf("initial model load wait = %s", initialModelLoadWait)
	}
}

func TestStartStartupReturnsWhenModelLoadCompletes(t *testing.T) {
	release := make(chan struct{})
	want := errors.New("remaining initialization completed")
	started := time.Now()
	command := startStartup(func(modelReady chan<- struct{}) runtimeInitializedMsg {
		close(modelReady)
		<-release
		return runtimeInitializedMsg{startupErr: want}
	}, time.Second)
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("completed model load waited %s", elapsed)
	}

	close(release)
	message, ok := command().(runtimeInitializedMsg)
	if !ok || !errors.Is(message.startupErr, want) {
		t.Fatalf("startup message = %#v", message)
	}
}

func TestStartStartupContinuesAfterWaitBudget(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	want := errors.New("completed asynchronously")

	begin := time.Now()
	command := startStartup(func(modelReady chan<- struct{}) runtimeInitializedMsg {
		close(started)
		<-release
		close(modelReady)
		return runtimeInitializedMsg{startupErr: want}
	}, 20*time.Millisecond)
	if elapsed := time.Since(begin); elapsed < 10*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("startup wait = %s", elapsed)
	}
	select {
	case <-started:
	default:
		t.Fatal("startup did not begin during the synchronous wait")
	}

	close(release)
	message, ok := command().(runtimeInitializedMsg)
	if !ok || !errors.Is(message.startupErr, want) {
		t.Fatalf("startup message = %#v", message)
	}
}
