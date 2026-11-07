// Package browsercontrol describes the user's CLI preference without owning a
// browser or granting additional tool permissions.
package browsercontrol

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

const preferenceTag = "<q_browser_control>"

type DriverStatus struct {
	ID              string   `json:"id"`
	Command         string   `json:"command"`
	Args            []string `json:"args"`
	ResolvedCommand string   `json:"resolved_command,omitempty"`
	Available       bool     `json:"available"`
	Error           string   `json:"error,omitempty"`
}

func Inspect(id string, driver config.BrowserCLI) DriverStatus {
	status := DriverStatus{ID: id, Command: driver.Command, Args: append([]string{}, driver.Args...)}
	command := driver.Command
	if id == "cdp-q-driver" && command == "" {
		var err error
		command, err = os.Executable()
		if err != nil {
			status.Error = err.Error()
			return status
		}
	}
	resolved, err := exec.LookPath(command)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.ResolvedCommand, status.Available = resolved, true
	return status
}

func Prompt(value config.BrowserControlConfig) string {
	value = value.Effective()
	if !value.Enabled {
		return ""
	}
	driver := Inspect(value.Preferred, value.Drivers[value.Preferred])
	body, _ := json.Marshal(driver)
	return "The user prefers this CLI for browser control. Find suitable skills yourself and reuse any complete instructions already in context. A skill is optional; use the CLI's help if no applicable skill exists. Use the configured executable and argument array through an available command tool, quoting them for the host shell. This preference does not grant unavailable tools. Reuse the intended browser session and clean up only resources owned by your task.\n" + string(body)
}

// AppendPreference delivers changes in user input so an existing stateful
// provider thread receives them without resetting its identity or prompt prefix.
func AppendPreference(messages []client.Message, preference string) []client.Message {
	if len(messages) == 0 || messages[len(messages)-1].Role != client.RoleUser {
		return messages
	}
	previous := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != client.RoleUser {
			continue
		}
		text := messages[i].TextContent()
		start := strings.LastIndex(text, preferenceTag)
		if start < 0 {
			continue
		}
		rest := text[start+len(preferenceTag):]
		end := strings.Index(rest, "</q_browser_control>")
		if end < 0 {
			continue
		}
		previous = strings.TrimSpace(rest[:end])
		break
	}
	const disabled = "The user has disabled the configured browser CLI preference. Earlier browser-control configuration is no longer active."
	wanted := preference
	if wanted == "" && previous != "" {
		wanted = disabled
	}
	if wanted == "" || wanted == previous {
		return messages
	}
	result := append([]client.Message(nil), messages...)
	last := len(result) - 1
	suffix := "\n\n" + preferenceTag + "\n" + wanted + "\n</q_browser_control>"
	if len(result[last].ContentParts) == 0 {
		result[last].Content += suffix
	} else {
		result[last].ContentParts = append(append([]client.MessageContentPart(nil), result[last].ContentParts...), client.MessageContentPart{"type": "text", "text": suffix})
	}
	return result
}
