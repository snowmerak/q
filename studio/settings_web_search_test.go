package studio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/systemoneconfig"
)

func TestWebSearchSettingsPersistAndPreserveExistingModels(t *testing.T) {
	store := transferTestStore(t)
	one := systemoneconfig.Default()
	one.RoleModels = map[string]string{systemoneconfig.RoleWebSearch: "typesafe/navigation", systemoneconfig.RoleWebSearchFinish: "typesafe/completion"}
	if err := (systemoneconfig.Store{Dir: store.Dir}).Save(one); err != nil {
		t.Fatal(err)
	}
	s := newSettingsService(store)
	before, err := s.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if before.WebSearch.MaxConcurrentReads != 2 || before.WebSearch.NavigationModel != "typesafe/navigation" || !before.WebSearch.Enabled {
		t.Fatalf("snapshot=%+v", before.WebSearch)
	}
	for _, body := range []string{`{"max_concurrent_reads":0,"generative_model":""}`, `{"max_concurrent_reads":9,"generative_model":""}`, `{"max_concurrent_reads":2,"generative_model":"group/chat"}`} {
		w := httptest.NewRecorder()
		s.serveWebSearchUpdate(w, httptest.NewRequest(http.MethodPut, "/api/v1/settings/web-search", strings.NewReader(body)))
		if w.Code != 422 {
			t.Fatalf("invalid request accepted: %d %s", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	s.serveWebSearchUpdate(w, httptest.NewRequest(http.MethodPut, "/api/v1/settings/web-search", strings.NewReader(`{"max_concurrent_reads":4,"generative_model":"local/search"}`)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	value, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if value.WebSearch.MaxConcurrentReads != 4 || value.WebSearch.GenerativeModel != "local/search" || value.Provider.Model != "local/model" {
		t.Fatalf("config=%+v", value.WebSearch)
	}
	var after settingsSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if after.WebSearch.CompletionModel != before.WebSearch.CompletionModel {
		t.Fatal("existing decision assignment changed")
	}
	state, err := s.loadTransferState()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := state.bundle()
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Sections["web-search"]["browser"] == nil || bundle.Sections["web-search"]["role/web_search"] == nil {
		t.Fatal("web search settings not exported")
	}
	var restored transferState
	restored.main = config.Default()
	restored.systemOne = systemoneconfig.Default()
	for id, data := range bundle.Sections["web-search"] {
		if err := restored.merge("web-search", id, data); err != nil {
			t.Fatal(err)
		}
	}
	if restored.main.WebSearch != value.WebSearch || restored.systemOne.RoleModels[systemoneconfig.RoleWebSearchFinish] != "typesafe/completion" {
		t.Fatal("web search transfer lost assignments")
	}
}
