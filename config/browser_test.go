package config

import "testing"

func TestBrowserConfigDefaultsIsolationAndValidation(t *testing.T) {
	value := BrowserControlConfig{Drivers: map[string]BrowserCLI{"ego-browser": {Command: "ego-browser", Args: []string{"profile one"}}}}
	effective := value.Effective()
	effective.Drivers["ego-browser"].Args[0] = "changed"
	if value.Drivers["ego-browser"].Args[0] != "profile one" || effective.Enabled || effective.Preferred != "cdp-q-driver" {
		t.Fatal("defaults mutate or enable configuration")
	}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []BrowserControlConfig{
		{Preferred: "unknown"},
		{Drivers: map[string]BrowserCLI{"cdp-q-driver": {}}},
		{Drivers: map[string]BrowserCLI{"ego-browser": {Command: ""}}},
		{Drivers: map[string]BrowserCLI{"ego-browser": {Command: "ego-browser\nother"}}},
		{Drivers: map[string]BrowserCLI{"ego-browser": {Command: "ego-browser", Args: []string{"bad\x00arg"}}}},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid config accepted: %#v", invalid)
		}
	}
}
