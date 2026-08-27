package app

import (
	"strings"
	"unicode"

	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

// ACP agents can advertise commands before session/new has returned, and when
// no turn is active. Keep those snapshots by session rather than dropping them.
func acpSlashCommands(commands []acp.AvailableCommand) []slashCommand {
	var result []slashCommand
	seen := make(map[string]bool)
	for _, command := range commands {
		name := strings.TrimPrefix(command.Name, "/")
		if name == "" || strings.ContainsRune(name, '/') || strings.ContainsFunc(name, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r)
		}) || seen[name] {
			continue
		}
		seen[name] = true
		item := slashCommand{name: "/" + name, description: singleLineCommandText(command.Description)}
		if command.Input != nil && command.Input.Unstructured != nil {
			hint := singleLineCommandText(command.Input.Unstructured.Hint)
			if hint == "" {
				hint = "input"
			}
			item.arguments = "<" + hint + ">"
		}
		result = append(result, item)
	}
	return result
}

func (r *acpRemoteClient) slashCommands() []slashCommand {
	r.mu.Lock()
	defer r.mu.Unlock()
	// /new is handled locally. All other completions must describe the remote
	// agent, not q's local settings screens, since they are forwarded unchanged.
	commands := []slashCommand{{name: "/new", description: "Start a new ACP session."}}
	hasClear := false
	for _, command := range r.slashCommandsBySession[r.sessionID] {
		if command.name == "/new" {
			continue
		}
		hasClear = hasClear || command.name == "/clear"
		commands = append(commands, command)
	}
	if !hasClear {
		commands = append(commands, slashCommand{name: "/clear", description: "Ask the connected agent to empty this session."})
	}
	return commands
}
