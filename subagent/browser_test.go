package subagent

import (
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestWorkerBrowserPreferenceIsContextOnlyAndRespectsCatalog(t *testing.T) {
	initial := []client.Message{{Role: client.RoleSystem, Content: "worker role"}, {Role: client.RoleUser, Content: "worker task"}}
	for _, command := range []bool{false, true} {
		var tools []client.Tool
		if command {
			tools = []client.Tool{{Type: client.ToolTypeFunction, Function: client.FunctionDefinition{Name: "run_command"}}}
		}
		memory := NewContextCompactor(Spec{BrowserPreference: "chosen CLI"}, initial, tools, 2)
		if strings.Contains(memory.Messages()[1].TextContent(), "chosen CLI") != command || initial[1].Content != "worker task" {
			t.Fatal("preference mutated transcript or ignored capability boundary")
		}
		memory.Append(client.Message{Role: client.RoleAssistant, Content: "working"}, client.Message{Role: client.RoleUser, Content: "follow up"})
		if memory.Messages()[3].Content != "follow up" {
			t.Fatal("unchanged preference repeated")
		}
	}
}
