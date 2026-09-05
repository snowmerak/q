package subagent

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
)

type fallbackCustomClient struct {
	models []string
}

func (c *fallbackCustomClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	c.models = append(c.models, request.Model)
	if request.Model == "primary" {
		return nil, &client.APIError{StatusCode: http.StatusServiceUnavailable, Message: "temporary"}
	}
	return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, Content: "fallback result"}}}}, nil
}

func customProfile() Profile {
	return Profile{Version: 1, Name: "inspector", Role: "analyst", SystemPrompt: "Inspect precisely.", Tools: []string{}}
}
func TestCustomRoleResolution(t *testing.T) {
	v := config.Default()
	v.Provider.Model = "test"
	v.Agents.Roles = map[string]config.AgentConfig{"analyst": {}}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	s, err := Resolve(v, "analyst", []client.Model{{ID: "test"}})
	if err != nil || s.Model != "test" {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err = Resolve(v, "typo", nil); err == nil {
		t.Fatal("unregistered role accepted")
	}
}
func TestCustomProfileStorage(t *testing.T) {
	s := ProfileStore{Global: filepath.Join(t.TempDir(), "global"), Workspace: filepath.Join(t.TempDir(), "workspace")}
	p := customProfile()
	if err := s.Save(p, "global", nil); err != nil {
		t.Fatal(err)
	}
	p.SystemPrompt = "Project"
	if err := s.Save(p, "workspace", nil); err != nil {
		t.Fatal(err)
	}
	e, err := s.Get(p.Name)
	if err != nil || e.Scope != "workspace" {
		t.Fatalf("%+v %v", e, err)
	}
	p.SystemPrompt = "Updated"
	if err = s.Save(p, "workspace", &e); err != nil {
		t.Fatal(err)
	}
	if err = s.Save(p, "workspace", &e); err == nil {
		t.Fatal("stale write accepted")
	}
	e, _ = s.Get(p.Name)
	if err = s.Delete(e); err != nil {
		t.Fatal(err)
	}
	e, err = s.Get(p.Name)
	if err != nil || e.Scope != "global" {
		t.Fatal("global did not reappear")
	}
	raw := []byte("version: 1\nname: inspector\nrole: analyst\nsystem_prompt: test\ntools: []\nunknown: true\n")
	if err = os.MkdirAll(s.Workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.Workspace, "bad.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(p.Name); err == nil {
		t.Fatal("invalid workspace fell back")
	}
}

func TestCustomProfileUnknownWorkspaceIdentityBlocksGlobalFallback(t *testing.T) {
	s := ProfileStore{Global: filepath.Join(t.TempDir(), "global"), Workspace: filepath.Join(t.TempDir(), "workspace")}
	p := customProfile()
	if err := s.Save(p, "global", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.Workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Workspace, p.Name+".yaml"), []byte("version: [\nname: inspector\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(p.Name); err == nil || !strings.Contains(err.Error(), "unreadable identity") {
		t.Fatalf("global fallback was not blocked: %v", err)
	}
}

func TestCustomProfileScopeMove(t *testing.T) {
	s := ProfileStore{Global: filepath.Join(t.TempDir(), "global"), Workspace: filepath.Join(t.TempDir(), "workspace")}
	p := customProfile()
	if err := s.Save(p, "global", nil); err != nil {
		t.Fatal(err)
	}
	original, _ := s.Get(p.Name)
	if err := s.Save(p, "workspace", &original); err != nil {
		t.Fatal(err)
	}
	e, err := s.Get(p.Name)
	if err != nil || e.Scope != "workspace" {
		t.Fatal("scope move failed", err)
	}
	if _, err = os.Stat(original.Path); !os.IsNotExist(err) {
		t.Fatal("source was not removed", err)
	}
	if err = s.Save(p, "global", nil); err != nil {
		t.Fatal(err)
	}
	if err = s.Save(p, "global", &e); err == nil {
		t.Fatal("scope move overwrote existing profile")
	}
	if _, err = os.Stat(e.Path); err != nil {
		t.Fatal("failed move removed source", err)
	}
}
func TestCustomRunnerSelectedToolsAndArchive(t *testing.T) {
	p := customProfile()
	p.Tools = []string{"read_file"}
	c := &fakeScoutClient{responses: []client.Message{{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall("write_file", "{}"), scoutCall("read_file", "{}")}}, {Role: client.RoleAssistant, Content: "done"}}}
	tools := &fakeScoutTools{available: []client.Tool{scoutFunctionTool("read_file"), scoutFunctionTool("write_file")}}
	sink := &scoutRecordSink{}
	out, err := (CustomRunner{Client: c, Tools: tools, Profile: p, Spec: Spec{Role: p.Role, Model: "test"}, Sink: sink, RunID: "test"}).Run(t.Context(), "inspect")
	if err != nil || out != "done" {
		t.Fatalf("%s %v", out, err)
	}
	if len(tools.calls) != 1 || tools.calls[0].Function.Name != "read_file" {
		t.Fatal(tools.calls)
	}
	if len(c.requests[0].Tools) != 1 || len(c.requests[0].Messages) < 2 ||
		c.requests[0].Messages[0].Content != p.SystemPrompt+"\n\nRuntime environment: \nWorking directory: " ||
		c.requests[0].Messages[1].Role != client.RoleUser {
		t.Fatal("incorrect profile injection")
	}
	if len(sink.records) == 0 || !strings.Contains(sink.records[0].Content, "inspector") {
		t.Fatal("missing profile snapshot")
	}
}

func TestCustomRunnerRecordsSelectedFallbackModel(t *testing.T) {
	p := customProfile()
	c := &fallbackCustomClient{}
	sink := &scoutRecordSink{}
	spec := Spec{
		Role: p.Role, Group: "fallback", Model: "primary", ReasoningEffort: "high",
		Router: client.NewModelRouter(),
		Candidates: []client.ModelCandidate{
			{Model: "primary", ReasoningEffort: "high"},
			{Model: "secondary", ReasoningEffort: "medium"},
		},
	}
	out, err := (CustomRunner{Client: c, Profile: p, Spec: spec, Sink: sink, RunID: "test"}).Run(t.Context(), "inspect")
	if err != nil || out != "fallback result" {
		t.Fatalf("%q %v", out, err)
	}
	if len(c.models) != 2 || c.models[0] != "primary" || c.models[1] != "secondary" {
		t.Fatalf("models = %#v", c.models)
	}
	for _, record := range sink.records {
		if record.Status == "succeeded" && record.Model != "secondary" {
			t.Fatalf("fallback model was not recorded: %#v", record)
		}
	}
}
func TestCustomRunnerEmptyLimitAndCancellation(t *testing.T) {
	p := customProfile()
	for _, tc := range []struct {
		name      string
		responses []client.Message
		rounds    int
		want      string
	}{{"empty", []client.Message{{Role: client.RoleAssistant}, {Role: client.RoleAssistant}}, 3, "empty"}, {"limit", []client.Message{{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{scoutCall("nope", "{}")}}}, 1, "exceeded"}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (CustomRunner{Client: &fakeScoutClient{responses: tc.responses}, Profile: p, Spec: Spec{Role: p.Role, Model: "test"}, MaxRounds: tc.rounds}).Run(t.Context(), "test")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := (CustomRunner{Client: &fakeScoutClient{}, Profile: p, Spec: Spec{Role: p.Role}}).Run(ctx, "test")
	if err == nil {
		t.Fatal("cancel ignored")
	}
}
