package studio

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/snowmerak/q/config"
)

func TestBrowserControlSaveReloadValidateAndTransfer(t *testing.T) {
	store := config.Store{Dir: t.TempDir()}
	handler, err := newHandler(store)
	if err != nil {
		t.Fatal(err)
	}
	update := config.BrowserControlConfig{Enabled: true, Preferred: "ego-browser", Drivers: map[string]config.BrowserCLI{"ego-browser": {Command: `C:\Program Files\Ego Browser\ego-browser.exe`, Args: []string{"--profile", "with spaces"}}}}
	response := serveJSON(t, handler, "PUT", "/api/v1/settings/browser-control", update)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var snapshot settingsSnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.BrowserControl.Config.Preferred != "ego-browser" || !strings.Contains(snapshot.BrowserControl.Prompt, "A skill is optional") || len(snapshot.BrowserControl.Drivers) != 3 {
		t.Fatalf("snapshot: %#v", snapshot.BrowserControl)
	}
	value, err := store.Load()
	if err != nil || !reflect.DeepEqual(value.BrowserControl, update) {
		t.Fatalf("saved config: %#v %v", value.BrowserControl, err)
	}
	before, _ := os.ReadFile(store.Path())
	invalid := update
	invalid.Preferred = "unknown"
	response = serveJSON(t, handler, "PUT", "/api/v1/settings/browser-control", invalid)
	if response.Code != 422 {
		t.Fatalf("invalid config accepted: %d", response.Code)
	}
	after, _ := os.ReadFile(store.Path())
	if string(before) != string(after) {
		t.Fatal("invalid request mutated config")
	}
	state := transferState{main: value}
	bundle, err := state.bundle()
	if err != nil {
		t.Fatal(err)
	}
	target := transferState{}
	if err := target.merge("runtime", "browser-control", bundle.Sections["runtime"]["browser-control"]); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(target.main.BrowserControl, update) {
		t.Fatal("transfer lost CLI arguments or paths")
	}
}
