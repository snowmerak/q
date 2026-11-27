package studio

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/snowmerak/q/config"
)

func TestUIStateSurvivesServerRestartAndPortChange(t *testing.T) {
	directory := t.TempDir()
	handler, err := newHandler(config.Store{Dir: directory})
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewServer(handler)
	patchUIState(t, first.URL, `{"values":{"q-studio-theme":"night","q-studio-text-scale":"130","q-studio-draft:session":"unsent text"}}`, http.StatusNoContent)

	// A fresh handler and origin must load the profile, not process memory.
	service := &uiStateService{path: filepath.Join(directory, "studio", "ui-state.json")}
	second := httptest.NewServer(uiStateMux(service))
	defer second.Close()
	first.Close()
	if first.URL == second.URL {
		t.Fatal("fixture did not change origin")
	}
	state := readUIState(t, second.URL)
	if *state.Values["q-studio-theme"] != "night" || *state.Values["q-studio-text-scale"] != "130" || *state.Values["q-studio-draft:session"] != "unsent text" {
		t.Fatalf("state was not restored: %+v", state)
	}
	patchUIState(t, second.URL, `{"values":{"q-studio-draft:session":null}}`, http.StatusNoContent)
	patchUIState(t, second.URL, `{"only_missing":true,"values":{"q-studio-theme":"light","q-studio-draft:session":"stale browser draft","q-studio-sidebar-collapsed":"false"}}`, http.StatusNoContent)
	state = readUIState(t, second.URL)
	if *state.Values["q-studio-theme"] != "night" || state.Values["q-studio-draft:session"] != nil || *state.Values["q-studio-sidebar-collapsed"] != "false" {
		t.Fatal("legacy migration overwrote saved state or resurrected a deleted draft")
	}
	if info, err := os.Stat(service.path); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("UI state permissions: %v %v", info, err)
	}
}

func TestUIStateRejectsInvalidUpdatesWithoutLosingData(t *testing.T) {
	service := &uiStateService{path: filepath.Join(t.TempDir(), "studio", "ui-state.json")}
	server := httptest.NewServer(uiStateMux(service))
	defer server.Close()
	patchUIState(t, server.URL, `{"values":{"q-studio-theme":"light"}}`, http.StatusNoContent)
	before, err := os.ReadFile(service.path)
	if err != nil {
		t.Fatal(err)
	}
	patchUIState(t, server.URL, `{"values":{"q-studio-theme":"night","unrelated":"value"}}`, http.StatusUnprocessableEntity)
	patchUIState(t, server.URL, `{"values":{"q-studio-theme":17}}`, http.StatusBadRequest)
	patchUIState(t, server.URL, `{"values":{"q-studio-theme":"night"},"unknown":true}`, http.StatusBadRequest)
	patchUIState(t, server.URL, `{"values":{"q-studio-draft:large":"`+strings.Repeat("x", (1<<20)+1)+`"}}`, http.StatusUnprocessableEntity)
	after, err := os.ReadFile(service.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected update changed saved preferences")
	}
	corrupt := []byte(`{"version":99,"values":{}}`)
	if err = os.WriteFile(service.path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	patchUIState(t, server.URL, `{"values":{"q-studio-theme":"night"}}`, http.StatusInternalServerError)
	after, err = os.ReadFile(service.path)
	if err != nil || !bytes.Equal(corrupt, after) {
		t.Fatal("unsupported state was silently overwritten")
	}
}

func TestUIStateConcurrentChangesMerge(t *testing.T) {
	service := &uiStateService{path: filepath.Join(t.TempDir(), "studio", "ui-state.json")}
	server := httptest.NewServer(uiStateMux(service))
	defer server.Close()
	var group sync.WaitGroup
	for index := range 16 {
		group.Go(func() {
			key := "q-studio-draft:" + strings.Repeat("x", index+1)
			body, _ := json.Marshal(map[string]any{"values": map[string]string{key: "draft"}})
			patchUIState(t, server.URL, string(body), http.StatusNoContent)
		})
	}
	group.Wait()
	if state := readUIState(t, server.URL); len(state.Values) != 16 {
		t.Fatalf("concurrent clients lost entries: %d", len(state.Values))
	}
}

func uiStateMux(service *uiStateService) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/ui-state", service.serveGet)
	mux.HandleFunc("PATCH /api/v1/ui-state", service.servePatch)
	return mux
}

func patchUIState(t *testing.T, origin, body string, status int) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPatch, origin+"/api/v1/ui-state", strings.NewReader(body))
	if err != nil {
		t.Error(err)
		return
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Error(err)
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != status {
		message, _ := io.ReadAll(response.Body)
		t.Errorf("UI state update: got %d, want %d: %s", response.StatusCode, status, message)
	}
}

func readUIState(t *testing.T, origin string) uiState {
	t.Helper()
	response, err := http.Get(origin + "/api/v1/ui-state")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("UI state response: %d %s", response.StatusCode, response.Header.Get("Cache-Control"))
	}
	var state uiState
	if err = json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	return state
}
