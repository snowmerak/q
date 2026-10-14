package studio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/snowmerak/q/sessionstore"
)

func TestStudioProjectSkillsStartWithEmbeddingProviderReady(t *testing.T) {
	for _, reconfigure := range []bool{false, true} {
		name := "fresh-project-skill"
		if reconfigure {
			name = "changed-skill-after-disabling-and-reenabling-embeddings"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newStudioIntegration(t)
			server := httptest.NewServer(fixture.handler)
			defer server.Close()
			directory := filepath.Join(fixture.root, ".agents", "skills", "project-review")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			writeSkill := func(description string) {
				t.Helper()
				body := "---\nname: project-review\ndescription: " + description + "\n---\n\nInspect source files.\n"
				if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			writeSkill("Review the project source.")
			turn := func() {
				t.Helper()
				run := fixture.start(t, "controlled finish")
				endpoint := server.URL + "/api/v1/sessions/" + fixture.session.SessionID + "/runs/" + run.ID + "/events?workspace_root=" + url.QueryEscape(fixture.root)
				page := waitStudioPage(t, endpoint, func(page studioRunPage) bool { return terminalRunStatus(page.Run.Status) })
				if page.Run.Status != "completed" {
					t.Fatalf("turn with project skill failed: %+v", page.Run)
				}
			}
			update := func(model string) {
				t.Helper()
				dimensions := 3
				if model == "" {
					dimensions = 0
				}
				response := serveJSON(t, fixture.handler, http.MethodPut, "/api/v1/settings/models/embedding", map[string]any{
					"model": model, "embedding_dimensions": dimensions, "workspace_root": fixture.root,
				})
				if response.Code != http.StatusAccepted {
					t.Fatalf("settings = %d %s", response.Code, response.Body.String())
				}
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					response := serveJSON(t, fixture.handler, http.MethodGet, "/api/v1/settings", nil)
					var value settingsSnapshot
					if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
						t.Fatal(err)
					}
					if value.Models.EmbeddingChange == nil && value.Models.EmbeddingModel == model {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("embedding model did not apply")
			}
			if reconfigure {
				turn() // First index the skill with embeddings disabled.
				update("default/embed-old")
				update("")
				writeSkill("Review changed project source.")
			}
			update("default/embed-current")
			turn()
			// A successful turn must also index the project skill with real vectors.
			response := serveJSON(t, fixture.handler, http.MethodGet, "/api/v1/embeddings/status?workspace_root="+url.QueryEscape(fixture.root), nil)
			var status embeddingStatusResponse
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d %s", response.Code, response.Body.String())
			}
			if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			for _, archive := range status.Archives {
				if archive.WorkspaceRoot != fixture.root {
					continue
				}
				for _, kind := range archive.Kinds {
					if kind.Kind == sessionstore.KindSkill && kind.Eligible == 1 && kind.Current == 1 {
						return
					}
				}
			}
			t.Fatalf("project skill was not embedded: %+v", status)
		})
	}
}
