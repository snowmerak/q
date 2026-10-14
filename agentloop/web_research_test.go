package agentloop_test

import (
	"testing"

	"github.com/snowmerak/q/agentloop"
)

func TestWebResearchInstructionsFollowToolAvailability(t *testing.T) {
	for _, test := range []struct {
		name  string
		tools []string
		want  bool
	}{
		{"configured research", []string{"browser_open", "browser_search"}, true},
		{"raw browser only", []string{"browser_open", "browser_read"}, false},
		{"no browser", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			messages := agentloop.PrepareWorkspaceMessages(nil, agentloop.WorkspaceMessageOptions{Tools: &scriptedTools{names: test.tools}})
			found := false
			for _, message := range messages {
				found = found || message.Name == "q_web_research"
			}
			if found != test.want {
				t.Fatalf("research instructions=%t, tool available=%t", found, test.want)
			}
		})
	}
}
