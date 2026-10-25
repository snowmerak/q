package studio

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/composer"
	"github.com/snowmerak/q/diagnostics"
	"github.com/snowmerak/q/worklock"
)

type composerService struct {
	store  *composer.Store
	runner composer.SessionRunner
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	active map[string]context.CancelFunc
	wg     sync.WaitGroup
}

func newComposerService(parent context.Context, dir string, runner composer.SessionRunner) *composerService {
	ctx, cancel := context.WithCancel(parent)
	return &composerService{store: composer.NewStore(dir), runner: runner, ctx: ctx, cancel: cancel, active: make(map[string]context.CancelFunc)}
}

func (s *composerService) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
	return nil
}

func writeComposerError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, composer.ErrInvalidID):
		status = http.StatusBadRequest
	case errors.Is(err, composer.ErrInvalidInput):
		status = http.StatusUnprocessableEntity
	case errors.Is(err, os.ErrNotExist):
		status = http.StatusNotFound
	case errors.Is(err, worklock.ErrLocked):
		status = http.StatusConflict
	}
	writeAPIError(w, status, err)
}

func (s *composerService) serveCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		values, err := s.store.List()
		if err != nil {
			writeComposerError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Composers []composer.Conversation `json:"composers"`
		}{values})
	case http.MethodPost:
		var input struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		}
		if err := decodeSettingsRequest(w, r, &input); err != nil {
			writeAPIError(w, http.StatusBadRequest, err)
			return
		}
		value, err := s.store.Create(input.Name, input.Model)
		if err != nil {
			writeComposerError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, value)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeAPIError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

// recover marks abandoned turns only while holding the process lock. A second
// Studio may read an active conversation but cannot mistake it for a crash.
func (s *composerService) recover(c *composer.Conversation) error {
	changed := false
	for i := range c.Turns {
		if c.Turns[i].Status == "running" {
			c.Turns[i].Status = "interrupted"
			c.Turns[i].Error = "Studio stopped before this reply finished. You can continue the conversation."
			changed = true
		}
	}
	if changed {
		c.UpdatedAt = time.Now().UTC()
		return s.store.Save(*c)
	}
	return nil
}

func (s *composerService) serveItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "GET, DELETE")
		writeAPIError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("composer")
	c, err := s.store.Load(id)
	if err != nil {
		writeComposerError(w, err)
		return
	}
	lock, err := s.store.Lock(id)
	if err != nil && (r.Method != http.MethodGet || !errors.Is(err, worklock.ErrLocked)) {
		writeComposerError(w, err)
		return
	}
	if lock != nil {
		defer func() { _ = lock.Close() }()
		// Reload after locking: another process may have completed a turn.
		c, err = s.store.Load(id)
		if err == nil {
			err = s.recover(&c)
		}
		if err != nil {
			writeComposerError(w, err)
			return
		}
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, c)
		return
	}
	if s.runner != nil {
		if err := s.runner.ReleaseSession(s.store.SessionStore(c), c.SessionID); err != nil {
			writeComposerError(w, err)
			return
		}
	}
	if err := s.store.Delete(id); err != nil {
		writeComposerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *composerService) serveMessages(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Content string `json:"content"`
	}
	if err := decodeSessionRequest(w, r, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" {
		writeAPIError(w, http.StatusUnprocessableEntity, errors.New("message is required"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil || s.runner == nil {
		writeAPIError(w, http.StatusServiceUnavailable, errors.New("composer runtime is unavailable"))
		return
	}
	id := r.PathValue("composer")
	lock, err := s.store.Lock(id)
	if err != nil {
		writeComposerError(w, err)
		return
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = lock.Close()
		}
	}()
	c, err := s.store.Load(id)
	if err == nil {
		err = s.recover(&c)
	}
	if err != nil {
		writeComposerError(w, err)
		return
	}
	turn := composer.Turn{ID: uuid.NewString(), Prompt: input.Content, Status: "running", CreatedAt: time.Now().UTC()}
	c.Turns = append(c.Turns, turn)
	c.UpdatedAt = turn.CreatedAt
	if err := s.store.Save(c); err != nil {
		writeComposerError(w, err)
		return
	}
	// The service owns accepted work; closing a browser request does not cancel it.
	ctx, cancel := context.WithCancel(diagnostics.Inherit(s.ctx, r.Context()))
	s.active[id] = cancel
	s.wg.Add(1)
	accepted = true
	go s.execute(ctx, cancel, lock, c)
	writeJSON(w, http.StatusAccepted, c)
}

func (s *composerService) execute(ctx context.Context, cancel context.CancelFunc, lock *worklock.Lock, c composer.Conversation) {
	// The HTTP response and worker must not share the mutable turn slice.
	c.Turns = append([]composer.Turn(nil), c.Turns...)
	defer s.wg.Done()
	defer cancel()
	logger := diagnostics.Logger(ctx).With("composer_id", c.ID, "turn_id", c.Turns[len(c.Turns)-1].ID, "session_id", c.SessionID)
	started := time.Now()
	logger.Info("composer.turn.started", "model", c.Model)
	turn := &c.Turns[len(c.Turns)-1]
	session := s.store.SessionStore(c)
	var published time.Time
	err := s.runner.RunWithOptions(ctx, session, c.SessionID, turn.Prompt, composer.Options(c.Model, turn.ID), func(event app.SessionEvent) error {
		if event.Type == "stream" && event.Kind == "response" {
			if event.Start {
				turn.Answer = ""
			}
			turn.Answer += event.Content
			// Publish bounded snapshots for polling and reconnects without syncing
			// the full history for every token.
			if time.Since(published) >= 750*time.Millisecond {
				if err := s.store.Save(c); err != nil {
					return err
				}
				published = time.Now()
			}
		}
		if event.Type == "result" {
			turn.Answer = event.Content
		}
		return nil
	})
	if err == nil && strings.TrimSpace(turn.Answer) == "" {
		err = errors.New("model returned an empty reply")
	}
	turn.Status = "completed"
	if err != nil {
		turn.Status, turn.Error = "failed", err.Error()
		if ctx.Err() != nil {
			turn.Status = "cancelled"
		}
	}
	// Release the native session before publishing completion so the next Studio
	// instance can immediately continue with the saved conversation context.
	if releaseErr := s.runner.ReleaseSession(session, c.SessionID); releaseErr != nil {
		logger.Error("composer.session.release_failed", "error", releaseErr)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c.UpdatedAt = time.Now().UTC()
	if saveErr := s.store.Save(c); saveErr != nil {
		logger.Error("composer.history.save_failed", "error", saveErr)
	}
	logger.Info("composer.turn.finished", "status", turn.Status, "duration_ms", time.Since(started).Milliseconds())
	delete(s.active, c.ID)
	if err := lock.Close(); err != nil {
		logger.Error("composer.lock.release_failed", "error", err)
	}
}

func (s *composerService) serveCancel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("composer")
	if !composer.ValidID(id) {
		writeComposerError(w, composer.ErrInvalidID)
		return
	}
	cancel, ok := s.active[id]
	if !ok {
		writeAPIError(w, http.StatusConflict, errors.New("no Composer reply is running in this Studio"))
		return
	}
	cancel()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancelling"})
}
