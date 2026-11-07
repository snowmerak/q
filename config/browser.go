package config

import (
	"fmt"
	"slices"
	"strings"
)

type BrowserCLI struct {
	Command string   `yaml:"command,omitempty" json:"command"`
	Args    []string `yaml:"args,omitempty" json:"args"`
}

type BrowserControlConfig struct {
	Enabled   bool                  `yaml:"enabled,omitempty" json:"enabled"`
	Preferred string                `yaml:"preferred,omitempty" json:"preferred"`
	Drivers   map[string]BrowserCLI `yaml:"drivers,omitempty" json:"drivers"`
}

func (c BrowserControlConfig) Effective() BrowserControlConfig {
	result := c
	if result.Preferred == "" {
		result.Preferred = "cdp-q-driver"
	}
	result.Drivers = map[string]BrowserCLI{
		"cdp-q-driver":   {Args: []string{"cdp"}},
		"ego-browser":    {Command: "ego-browser"},
		"playwright-cli": {Command: "playwright-cli"},
	}
	for id, driver := range c.Drivers {
		driver.Args = slices.Clone(driver.Args)
		result.Drivers[id] = driver
	}
	return result
}

func (c BrowserControlConfig) Validate() error {
	c = c.Effective()
	if _, ok := c.Drivers[c.Preferred]; !ok {
		return fmt.Errorf("browser_control: unknown preferred CLI %q", c.Preferred)
	}
	for id, driver := range c.Drivers {
		if id == "" || len(id) > 80 || strings.TrimSpace(id) != id {
			return fmt.Errorf("browser_control: invalid CLI name %q", id)
		}
		if strings.ContainsRune(driver.Command, 0) || strings.ContainsAny(driver.Command, "\r\n") {
			return fmt.Errorf("browser_control: invalid executable for %q", id)
		}
		if driver.Command == "" && id != "cdp-q-driver" {
			return fmt.Errorf("browser_control: executable is required for %q", id)
		}
		if driver.Command == "" && id == "cdp-q-driver" && (len(driver.Args) == 0 || driver.Args[0] != "cdp") {
			return fmt.Errorf("browser_control: the built-in CLI requires cdp as its first argument")
		}
		for _, arg := range driver.Args {
			if strings.ContainsRune(arg, 0) {
				return fmt.Errorf("browser_control: invalid argument for %q", id)
			}
		}
	}
	return nil
}
