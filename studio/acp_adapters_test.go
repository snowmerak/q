package studio

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/internal/acpadapter"
)

func TestStudioACPAdapterAvailabilityAndInstallPreconditions(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	handler, err := newHandler(config.Store{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	response := serveJSON(t, handler, http.MethodGet, "/api/v1/settings/acp/adapters", nil)
	if response.Code != 200 {
		t.Fatalf("availability = %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Adapters []acpadapter.Status `json:"adapters"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Adapters) != 3 {
		t.Fatalf("presets = %+v", body.Adapters)
	}
	for _, status := range body.Adapters {
		if status.CanInstall || status.Installed {
			t.Fatalf("unavailable runtime = %+v", status)
		}
	}
	for _, preset := range []string{"codex", "claude", "grok", "@other/package"} {
		response := serveJSON(t, handler, http.MethodPost, "/api/v1/settings/acp/adapters", map[string]string{"preset": preset})
		if response.Code != 422 {
			t.Fatalf("install %q = %d %s", preset, response.Code, response.Body.String())
		}
	}
}
