package app

import (
	"errors"
	"testing"
	"time"
)

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
