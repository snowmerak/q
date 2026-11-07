package browsercontrol

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

func TestPreferenceUpdatesPreserveHistoryAndRichInput(t *testing.T) {
	initial := []client.Message{{Role: client.RoleUser, Content: "original"}}
	first := AppendPreference(initial, "CLI one")
	if initial[0].Content != "original" || !strings.Contains(first[0].Content, "CLI one") {
		t.Fatal("input mutated or preference missing")
	}
	before, _ := json.Marshal(first)
	history := append(first, client.Message{Role: client.RoleUser, ContentParts: []client.MessageContentPart{{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,test"}}, {"type": "text", "text": "next"}}})
	unchanged := AppendPreference(history, "CLI one")
	if len(unchanged[1].ContentParts) != 2 {
		t.Fatal("unchanged preference repeated")
	}
	updated := AppendPreference(history, "CLI two")
	if len(updated[1].ContentParts) != 3 || len(history[1].ContentParts) != 2 || !strings.Contains(updated[1].TextContent(), "CLI two") {
		t.Fatal("rich input or preference update lost")
	}
	after, _ := json.Marshal(updated[:1])
	if string(before) != string(after) {
		t.Fatal("history prefix changed")
	}
	disabled := AppendPreference(append(updated, client.Message{Role: client.RoleUser, Content: "third"}), "")
	if !strings.Contains(disabled[2].Content, "disabled") {
		t.Fatal("old preference was not cleared")
	}
	last := AppendPreference(append(disabled, client.Message{Role: client.RoleUser, Content: "fourth"}), "")
	if last[3].Content != "fourth" {
		t.Fatal("disabled preference repeated")
	}
}

func TestPromptAndExecutableResolution(t *testing.T) {
	if Prompt(config.BrowserControlConfig{}) != "" {
		t.Fatal("disabled preference produced instructions")
	}
	value := config.BrowserControlConfig{Enabled: true}
	status := Inspect("cdp-q-driver", value.Effective().Drivers["cdp-q-driver"])
	executable, _ := os.Executable()
	if !status.Available || status.ResolvedCommand != executable || len(status.Args) != 1 || status.Args[0] != "cdp" {
		t.Fatalf("embedded command: %#v", status)
	}
	if !strings.Contains(Prompt(value), "A skill is optional") {
		t.Fatal("preference requires a skill")
	}
	if Inspect("missing", config.BrowserCLI{Command: "q-missing-browser-executable-12345"}).Available {
		t.Fatal("missing command reported available")
	}
}
