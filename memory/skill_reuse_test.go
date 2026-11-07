package memory

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
)

func TestSkillReuseRequiresExactResourceStillInContext(t *testing.T) {
	loaded := skillReadMessages("first-read", "one", "SKILL.md", "complete instructions")[1]
	current := loaded
	current.ToolCallID = "new-read"
	changed := skillReadMessages("changed-read", "one", "SKILL.md", "updated instructions")[1]
	reference := skillReadMessages("reference-read", "one", "references/guide.md", "complete instructions")[1]
	short, ok := ReuseSkillResource(current, []client.Message{loaded})
	if !ok {
		t.Fatal("unchanged resource was not reused")
	}
	rich := loaded
	rich.Content, rich.ContentParts = "", []client.MessageContentPart{{"type": "text", "text": loaded.Content}}
	withDigest := func(m client.Message, digest string) client.Message {
		var out map[string]any
		if err := json.Unmarshal([]byte(m.Content), &out); err != nil {
			t.Fatal(err)
		}
		out["skill"].(map[string]any)["digest"] = digest
		body, _ := json.Marshal(out)
		m.Content = string(body)
		return m
	}
	for _, tt := range []struct {
		name    string
		message client.Message
		context []client.Message
		reuse   bool
	}{
		{"present", current, []client.Message{loaded}, true},
		{"different session", current, nil, false},
		{"body evicted but reference remains", current, []client.Message{short}, false},
		{"same body in rich history", current, []client.Message{rich}, true},
		{"changed body", changed, []client.Message{loaded}, false},
		{"changed digest", withDigest(current, "new"), []client.Message{withDigest(loaded, "old")}, false},
		{"different resource", reference, []client.Message{loaded}, false},
		{"resource reverted", current, []client.Message{loaded, changed}, false},
		{"error", client.Message{Role: client.RoleTool, Name: "get_skill", Content: "Tool error: unavailable"}, []client.Message{loaded}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, reused := ReuseSkillResource(tt.message, tt.context)
			if reused != tt.reuse {
				t.Fatalf("reused=%v, want %v", reused, tt.reuse)
			}
			if !reused {
				if got.TextContent() != tt.message.TextContent() {
					t.Fatal("full response was changed")
				}
				return
			}
			var result struct {
				Already  bool    `json:"already_in_context"`
				Previous string  `json:"previous_tool_call_id"`
				Content  *string `json:"content"`
			}
			if err := json.Unmarshal([]byte(got.Content), &result); err != nil {
				t.Fatal(err)
			}
			if !result.Already || result.Previous != loaded.ToolCallID || result.Content != nil || got.ToolCallID != tt.message.ToolCallID || strings.Contains(got.Content, "complete instructions") {
				t.Fatalf("invalid reference: %+v %s", result, got.Content)
			}
			if _, body := skillResourceKey(got.Content); body {
				t.Fatal("reference would replace the retained full body")
			}
		})
	}
}

func TestSkillReuseSurvivesRetentionWithoutReplacingBodyWithReference(t *testing.T) {
	reads := skillReadMessages("first", "one", "SKILL.md", "Exact retained instructions")
	duplicate := skillReadMessages("second", "one", "SKILL.md", "Exact retained instructions")
	duplicate[1], _ = ReuseSkillResource(duplicate[1], reads)
	_, retained := splitSkillResourceReads(append(reads, duplicate...), 0, 16000)
	if len(retained) != 2 || retained[1].ToolCallID != "first" || !strings.Contains(retained[1].Content, "Exact retained instructions") {
		t.Fatalf("retained wrong resource: %+v", retained)
	}
	if _, reused := ReuseSkillResource(duplicate[1], retained); reused {
		t.Fatal("a reference was mistaken for a resource body")
	}
}
