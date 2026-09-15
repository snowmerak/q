package app

import (
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestKnownSkillIDsRestoresHintsAndLoadedSkillsFromContext(t *testing.T) {
	user := appendSkillHintContext(client.Message{Role: client.RoleUser, Content: "review this"}, &skillHintSet{
		Trigger: "user_input", Candidates: []skillHint{{ID: "from-user", Name: "user"}},
	})
	messages := []client.Message{
		user,
		{Role: client.RoleTool, Name: taskStartToolName, Content: `{"skill_hints":{"trigger":"task_start","candidates":[{"id":"from-task","name":"task","scope":"global"}]}}`},
		{Role: client.RoleTool, Name: "get_skill", Content: `{"skill":{"id":"loaded"},"path":"SKILL.md","content":"full text"}`},
		{Role: client.RoleTool, Name: askToUserToolName, Content: "malformed"},
	}

	known := knownSkillIDs(messages)
	for _, id := range []string{"from-user", "from-task", "loaded"} {
		if _, ok := known[id]; !ok {
			t.Fatalf("known skill IDs %#v omitted %q", known, id)
		}
	}
	if count := strings.Count(user.Content, skillHintsTag); count != 1 {
		t.Fatalf("user hint tags = %d in %q", count, user.Content)
	}
}
