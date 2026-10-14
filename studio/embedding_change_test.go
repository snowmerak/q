package studio

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/workspace"
)

type deferredEmbeddingRuntime struct {
	busy    atomic.Bool
	fail    atomic.Bool
	entered chan struct{}
}

type supersededEmbeddingRuntime struct {
	deferredEmbeddingRuntime
	first    atomic.Bool
	started  chan struct{}
	complete chan struct{}
}

func (runtime *supersededEmbeddingRuntime) ApplyEmbeddingChange(ctx context.Context, _ string, commit func() (config.Config, error)) error {
	if _, err := commit(); err != nil {
		return err
	}
	if !runtime.first.Swap(true) {
		close(runtime.started)
		select {
		case <-runtime.complete:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func embeddingChangeTestConfig() config.Config {
	value := config.Default()
	value.Provider.Model = "test-model"
	return value
}

func (*deferredEmbeddingRuntime) ApplyGateway(context.Context, gateway.Config) error { return nil }

func (runtime *deferredEmbeddingRuntime) ApplyEmbeddingChange(ctx context.Context, _ string, commit func() (config.Config, error)) error {
	if runtime.busy.Load() {
		return workspace.ErrLocked
	}
	if _, err := commit(); err != nil {
		return err
	}
	if runtime.entered != nil {
		close(runtime.entered)
		<-ctx.Done()
		return ctx.Err()
	}
	if runtime.fail.Load() {
		return errors.New("migration fixture unavailable")
	}
	return nil
}

func queueTestEmbeddingChange(t *testing.T, settings *settingsService, model string) {
	t.Helper()
	response := httptest.NewRecorder()
	settings.queueEmbeddingChange(response, modelAssignmentUpdate{Model: model, EmbeddingDimensions: 3})
	if response.Code != http.StatusAccepted {
		t.Fatalf("queue change = %d %s", response.Code, response.Body.String())
	}
}

func waitEmbeddingChange(t *testing.T, settings *settingsService, ready func(*embeddingChange) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		settings.mu.Lock()
		change, err := settings.loadEmbeddingChange()
		settings.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if ready(change) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("embedding change did not reach expected state")
}

func TestEmbeddingChangeWaitsPersistsAndUsesLatestRequestAfterRestart(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := embeddingChangeTestConfig()
	value.Embedding = config.EmbeddingConfig{Model: "old", Dimensions: 3}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	runtime := &deferredEmbeddingRuntime{}
	runtime.busy.Store(true)
	settings := newSettingsService(store, runtime)
	settings.embeddingChanges = newEmbeddingChangeService(t.Context(), settings)
	queueTestEmbeddingChange(t, settings, "superseded")
	queueTestEmbeddingChange(t, settings, "latest")
	if err := settings.embeddingChanges.Close(); err != nil {
		t.Fatal(err)
	}
	unchanged, err := store.Load()
	if err != nil || unchanged.Embedding.Model != "old" {
		t.Fatalf("shutdown before idle changed model: %+v, %v", unchanged.Embedding, err)
	}
	// The pending request must not replace unrelated settings edited while busy.
	unchanged.Agents.MaxParallel = 7
	if err := store.Save(unchanged); err != nil {
		t.Fatal(err)
	}
	restarted := newSettingsService(store, runtime)
	restarted.embeddingChanges = newEmbeddingChangeService(t.Context(), restarted)
	t.Cleanup(func() { _ = restarted.embeddingChanges.Close() })
	restarted.mu.Lock()
	snapshot, err := restarted.snapshot()
	restarted.mu.Unlock()
	if err != nil || snapshot.Models.EmbeddingModel != "old" || snapshot.Models.EmbeddingChange == nil || snapshot.Models.EmbeddingChange.Model != "latest" {
		t.Fatalf("restart lost unapplied request: %+v, %v", snapshot.Models, err)
	}
	runtime.busy.Store(false)
	restarted.embeddingChanges.wake <- struct{}{}
	waitEmbeddingChange(t, restarted, func(change *embeddingChange) bool { return change == nil })
	applied, err := store.Load()
	if err != nil || applied.Embedding.Model != "latest" || applied.Agents.MaxParallel != 7 {
		t.Fatalf("applied config = %+v, %v", applied, err)
	}
}

func TestEmbeddingChangeFailureRemainsVisibleAndRetryCompletes(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	if err := store.Save(embeddingChangeTestConfig()); err != nil {
		t.Fatal(err)
	}
	runtime := &deferredEmbeddingRuntime{}
	runtime.fail.Store(true)
	settings := newSettingsService(store, runtime)
	settings.embeddingChanges = newEmbeddingChangeService(t.Context(), settings)
	t.Cleanup(func() { _ = settings.embeddingChanges.Close() })
	queueTestEmbeddingChange(t, settings, "new")
	waitEmbeddingChange(t, settings, func(change *embeddingChange) bool {
		return change != nil && change.State == "failed" && change.Error != ""
	})
	runtime.fail.Store(false)
	settings.embeddingChanges.wake <- struct{}{}
	waitEmbeddingChange(t, settings, func(change *embeddingChange) bool { return change == nil })
}

func TestEmbeddingChangeCancelledMigrationIsNotMarkedComplete(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	if err := store.Save(embeddingChangeTestConfig()); err != nil {
		t.Fatal(err)
	}
	runtime := &deferredEmbeddingRuntime{entered: make(chan struct{})}
	settings := newSettingsService(store, runtime)
	settings.embeddingChanges = newEmbeddingChangeService(t.Context(), settings)
	queueTestEmbeddingChange(t, settings, "new")
	select {
	case <-runtime.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("migration did not start")
	}
	if err := settings.embeddingChanges.Close(); err != nil {
		t.Fatal(err)
	}
	change, err := settings.loadEmbeddingChange()
	if err != nil || change == nil || change.State != "pending" {
		t.Fatalf("cancelled migration was cleared: %+v, %v", change, err)
	}
	resumed := newSettingsService(store, &deferredEmbeddingRuntime{})
	resumed.embeddingChanges = newEmbeddingChangeService(t.Context(), resumed)
	t.Cleanup(func() { _ = resumed.embeddingChanges.Close() })
	waitEmbeddingChange(t, resumed, func(change *embeddingChange) bool { return change == nil })
}

func TestEmbeddingImportReplacesPendingRequestWithoutChangingActiveModel(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	value := embeddingChangeTestConfig()
	value.Embedding = config.EmbeddingConfig{Model: "old", Dimensions: 3}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	runtime := &deferredEmbeddingRuntime{}
	runtime.busy.Store(true)
	settings := newSettingsService(store, runtime)
	settings.embeddingChanges = newEmbeddingChangeService(t.Context(), settings)
	t.Cleanup(func() { _ = settings.embeddingChanges.Close() })
	queueTestEmbeddingChange(t, settings, "superseded")
	bundle := transferTestBundle(t, map[string]map[string]any{"models": {
		"embedding": transferEmbedding{Model: "imported", Dimensions: 3},
	}})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /import", settings.serveTransferImport)
	response := transferRequest(t, mux, "/import", bundle)
	if response.Code != http.StatusOK {
		t.Fatalf("import = %d %s", response.Code, response.Body.String())
	}
	settings.mu.Lock()
	snapshot, err := settings.snapshot()
	settings.mu.Unlock()
	if err != nil || snapshot.Models.EmbeddingModel != "old" || snapshot.Models.EmbeddingChange == nil || snapshot.Models.EmbeddingChange.Model != "imported" {
		t.Fatalf("import did not defer replacement: %+v, %v", snapshot.Models, err)
	}
}

func TestEmbeddingChangeCompletionDoesNotClearNewerRequest(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	if err := store.Save(embeddingChangeTestConfig()); err != nil {
		t.Fatal(err)
	}
	runtime := &supersededEmbeddingRuntime{started: make(chan struct{}), complete: make(chan struct{})}
	settings := newSettingsService(store, runtime)
	settings.embeddingChanges = newEmbeddingChangeService(t.Context(), settings)
	t.Cleanup(func() { _ = settings.embeddingChanges.Close() })
	queueTestEmbeddingChange(t, settings, "first")
	select {
	case <-runtime.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first migration did not start")
	}
	queueTestEmbeddingChange(t, settings, "latest")
	close(runtime.complete)
	waitEmbeddingChange(t, settings, func(change *embeddingChange) bool { return change == nil })
	value, err := store.Load()
	if err != nil || value.Embedding.Model != "latest" {
		t.Fatalf("old completion discarded newer request: %+v, %v", value.Embedding, err)
	}
}
