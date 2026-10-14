package studio

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/workspace"
)

type embeddingChange struct {
	ID            string    `json:"id"`
	Model         string    `json:"model"`
	Dimensions    int       `json:"dimensions"`
	WorkspaceRoot string    `json:"workspace_root,omitempty"`
	State         string    `json:"state"`
	RequestedAt   time.Time `json:"requested_at"`
	Error         string    `json:"error,omitempty"`
}

type embeddingChangeRuntime interface {
	ApplyEmbeddingChange(context.Context, string, func() (config.Config, error)) error
}

func newEmbeddingChange(value config.EmbeddingConfig, root string) *embeddingChange {
	return &embeddingChange{ID: rand.Text(), Model: value.Model, Dimensions: value.Dimensions,
		WorkspaceRoot: root, State: "pending", RequestedAt: time.Now().UTC()}
}

type embeddingChangeService struct {
	settings *settingsService
	runtime  embeddingChangeRuntime
	ctx      context.Context
	cancel   context.CancelFunc
	wake     chan struct{}
	done     chan struct{}
}

func newEmbeddingChangeService(parent context.Context, settings *settingsService) *embeddingChangeService {
	runtime, ok := settings.runtime.(embeddingChangeRuntime)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	service := &embeddingChangeService{settings: settings, runtime: runtime, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go service.run()
	return service
}

func (service *embeddingChangeService) Close() error {
	if service != nil {
		service.cancel()
		<-service.done
	}
	return nil
}

func (service *settingsService) embeddingChangePath() string {
	return filepath.Join(service.main.Dir, "embedding-change.json")
}

func (service *settingsService) loadEmbeddingChange() (*embeddingChange, error) {
	body, err := os.ReadFile(service.embeddingChangePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var change embeddingChange
	if err := json.Unmarshal(body, &change); err != nil {
		return nil, fmt.Errorf("read pending embedding change: %w", err)
	}
	return &change, nil
}

func (service *settingsService) saveEmbeddingChange(change *embeddingChange) error {
	body, err := json.Marshal(change)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(service.main.Dir, 0o700); err != nil {
		return err
	}
	return fsreplace.WriteFile(service.embeddingChangePath(), body, 0o600)
}

func (service *settingsService) queueEmbeddingChange(writer http.ResponseWriter, update modelAssignmentUpdate) {
	root, err := optionalCanonicalWorkspaceDirectory(update.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusConflict, err)
		return
	}
	value.Embedding = config.EmbeddingConfig{Model: update.Model, Dimensions: update.EmbeddingDimensions}
	if value.Embedding.Model == "" {
		value.Embedding.Dimensions = 0
	}
	if err := value.Validate(); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	change := newEmbeddingChange(value.Embedding, root)
	if err := service.saveEmbeddingChange(change); err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	select {
	case service.embeddingChanges.wake <- struct{}{}:
	default:
	}
	snapshot, err := service.snapshot()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, snapshot)
}

func (service *embeddingChangeService) run() {
	defer close(service.done)
	timer := time.NewTimer(0) // Resume an unapplied request after a restart.
	defer timer.Stop()
	for {
		select {
		case <-service.ctx.Done():
			return
		case <-service.wake:
		case <-timer.C:
		}
		err := service.apply()
		if err == nil {
			timer.Stop() // No pending work: wait for another request instead of polling disk.
			continue
		}
		delay := time.Second
		if !errors.Is(err, workspace.ErrLocked) {
			delay = 30 * time.Second
		}
		timer.Reset(delay)
	}
}

func (service *embeddingChangeService) apply() error {
	settings := service.settings
	settings.mu.Lock()
	change, err := settings.loadEmbeddingChange()
	settings.mu.Unlock()
	if err != nil || change == nil {
		return err
	}
	err = service.runtime.ApplyEmbeddingChange(service.ctx, change.WorkspaceRoot, func() (config.Config, error) {
		settings.mu.Lock()
		defer settings.mu.Unlock()
		if err := service.ctx.Err(); err != nil {
			return config.Config{}, err
		}
		current, err := settings.loadEmbeddingChange()
		if err != nil {
			return config.Config{}, err
		}
		// A newer request supersedes work that has not committed its model yet.
		if current == nil || current.ID != change.ID {
			return config.Config{}, workspace.ErrLocked
		}
		value, err := settings.main.Load()
		if err != nil {
			return config.Config{}, err
		}
		change.State, change.Error = "applying", ""
		if err := settings.saveEmbeddingChange(change); err != nil {
			return config.Config{}, err
		}
		value.Embedding = config.EmbeddingConfig{Model: change.Model, Dimensions: change.Dimensions}
		if err := settings.main.Save(value); err != nil {
			return config.Config{}, err
		}
		return value, nil
	})
	settings.mu.Lock()
	defer settings.mu.Unlock()
	current, loadErr := settings.loadEmbeddingChange()
	if loadErr != nil {
		return errors.Join(err, loadErr)
	}
	if current == nil || current.ID != change.ID {
		return err
	}
	if err == nil {
		// Only a completed migration clears the durable request. Cancellation,
		// partial migration and process exit leave enough state to retry.
		return os.Remove(settings.embeddingChangePath())
	}
	if errors.Is(err, workspace.ErrLocked) {
		return err
	}
	change.State, change.Error = "failed", err.Error()
	if service.ctx.Err() != nil {
		change.State, change.Error = "pending", ""
	}
	return errors.Join(err, settings.saveEmbeddingChange(change))
}
