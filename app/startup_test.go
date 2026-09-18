package app

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/config"
)

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
