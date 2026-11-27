package studio

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/internal/strictjson"
)

const maximumUIStateSize = 8 << 20

type uiState struct {
	Version int                `json:"version"`
	Values  map[string]*string `json:"values"`
}

type uiStateService struct {
	mu   sync.Mutex
	path string
}

func (service *uiStateService) load() (uiState, error) {
	state := uiState{Version: 1, Values: make(map[string]*string)}
	file, err := os.Open(service.path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read Studio UI state: %w", err)
	}
	defer func() { _ = file.Close() }()
	body, err := io.ReadAll(io.LimitReader(file, maximumUIStateSize+1))
	if err != nil {
		return state, fmt.Errorf("read Studio UI state: %w", err)
	}
	if len(body) > maximumUIStateSize {
		return state, errors.New("studio UI state exceeds its size limit")
	}
	if err = strictjson.Decode(bytes.NewReader(body), &state); err != nil {
		return state, fmt.Errorf("decode Studio UI state: %w", err)
	}
	if state.Version != 1 || state.Values == nil {
		return state, errors.New("unsupported Studio UI state format")
	}
	return state, nil
}

func (service *uiStateService) serveGet(writer http.ResponseWriter, _ *http.Request) {
	service.mu.Lock()
	defer service.mu.Unlock()
	state, err := service.load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, state)
}

func (service *uiStateService) servePatch(writer http.ResponseWriter, request *http.Request) {
	var update struct {
		Values      map[string]*string `json:"values"`
		OnlyMissing bool               `json:"only_missing,omitempty"`
	}
	if err := decodeRequest(writer, request, &update, maximumUIStateSize, "decode UI state"); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if len(update.Values) == 0 {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("UI state update is empty"))
		return
	}
	for key, value := range update.Values {
		if !validUIStateKey(key) || value != nil && len(*value) > 1<<20 {
			writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("invalid UI state key or value size"))
			return
		}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	state, err := service.load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	for key, value := range update.Values {
		if _, exists := state.Values[key]; update.OnlyMissing && exists {
			continue
		}
		// Keep deletion markers so migration from another old browser origin
		// cannot resurrect a draft that was already cleared in this profile.
		state.Values[key] = value
	}
	body, err := json.Marshal(state)
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	if len(body) > maximumUIStateSize {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("studio UI state storage limit reached"))
		return
	}
	if err = os.MkdirAll(filepath.Dir(service.path), 0o700); err == nil {
		err = fsreplace.WriteFile(service.path, body, 0o600)
	}
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, fmt.Errorf("save Studio UI state: %w", err))
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

func validUIStateKey(key string) bool {
	if len(key) > 4096 {
		return false
	}
	switch key {
	case "q-studio-theme", "q-studio-text-scale", "q-studio-compact-sessions", "q-studio-sidebar-collapsed", "q-studio-workspace-root":
		return true
	default:
		return strings.HasPrefix(key, "q-studio-draft:") && len(key) > len("q-studio-draft:")
	}
}
