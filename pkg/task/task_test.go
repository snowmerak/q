package task_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	sdk "github.com/snowmerak/decisions"
	"github.com/snowmerak/q/v2/pkg/app/config"
	"github.com/snowmerak/q/v2/pkg/domain"
	"github.com/snowmerak/q/v2/pkg/storage"
	"github.com/snowmerak/q/v2/pkg/storage/blob/localfs"
	"github.com/snowmerak/q/v2/pkg/storage/rdb/sqlite"
	"github.com/snowmerak/q/v2/pkg/storage/search/blevehnsw"
	"github.com/snowmerak/q/v2/pkg/task"
)

type stubDecisionEval struct {
	fn func(ctx context.Context, modelID string, input sdk.Input, q sdk.Questions) (*sdk.Response, error)
}

func (s stubDecisionEval) Evaluate(ctx context.Context, modelID string, input sdk.Input, q sdk.Questions) (*sdk.Response, error) {
	return s.fn(ctx, modelID, input, q)
}

func TestApprovalGateModesAndFailClosed(t *testing.T) {
	spec := task.ToolSpec{Name: "run_command", Source: domain.ToolSourceBuiltin, Risk: task.RiskExec}

	// Precedence: toolOverride > roomOverride > charOverride > global
	roomOv := domain.ApprovalRequest
	charOv := domain.ApprovalBypass
	gotMode := task.EffectiveMode(domain.ApprovalAutoDetection, &roomOv, &charOv, map[string]domain.ApprovalMode{
		"run_command": domain.ApprovalBypass,
	}, "run_command")
	if gotMode != domain.ApprovalBypass {
		t.Fatalf("EffectiveMode = %s, want bypass", gotMode)
	}

	if req, _ := task.EvaluateApproval(t.Context(), nil, "", domain.ApprovalBypass, spec, "{}", "hi"); req {
		t.Fatal("bypass mode must not require approval")
	}
	if req, _ := task.EvaluateApproval(t.Context(), nil, "", domain.ApprovalRequest, spec, "{}", "hi"); !req {
		t.Fatal("request mode must require approval")
	}

	// auto-detection: fail-closed when Decisions errors or is nil
	if req, reason := task.EvaluateApproval(t.Context(), nil, "d1", domain.ApprovalAutoDetection, spec, "{}", "hi"); !req || !strings.Contains(reason, "fail_closed") {
		t.Fatalf("expected fail-closed when Decisions nil, got req=%v reason=%q", req, reason)
	}
	errDec := stubDecisionEval{fn: func(context.Context, string, sdk.Input, sdk.Questions) (*sdk.Response, error) {
		return nil, errors.New("upstream 503")
	}}
	if req, reason := task.EvaluateApproval(t.Context(), errDec, "d1", domain.ApprovalAutoDetection, spec, "{}", "hi"); !req || !strings.Contains(reason, "fail_closed") {
		t.Fatalf("expected fail-closed on Decisions error, got req=%v reason=%q", req, reason)
	}

	// auto-detection allowing low-risk read
	readSpec := task.ToolSpec{Name: "read_file", Source: domain.ToolSourceBuiltin, Risk: task.RiskRead}
	allowDec := stubDecisionEval{fn: func(context.Context, string, sdk.Input, sdk.Questions) (*sdk.Response, error) {
		return &sdk.Response{Answers: map[string]sdk.Answer{
			"requires_approval": sdk.NoulAnswer{Probability: 0.1},
		}}, nil
	}}
	if req, _ := task.EvaluateApproval(t.Context(), allowDec, "d1", domain.ApprovalAutoDetection, readSpec, `{"path":"a.txt"}`, "read a.txt"); req {
		t.Fatal("expected auto-detection false -> allowed")
	}
}

func TestFileSandboxHashAnchorsAndInternalProtection(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "work")
	dataDir := filepath.Join(base, "work", "qdata")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dataDir, "q.sqlite"), []byte("secret-db"), 0o600)
	_ = os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0o600)

	reg := task.NewRegistry(task.RegistryDeps{})
	call := task.CallContext{
		AllowedRoots: []string{workspace},
		DataDir:      dataDir,
	}

	// 1. write_file -> read_file (LINE#HASH) -> edit_file
	out, err := reg.Execute(t.Context(), call, "write_file", `{"path":"note.txt","content":"alpha\nbeta\ngamma"}`)
	if err != nil || out.IsError {
		t.Fatalf("write_file: %+v err=%v", out, err)
	}
	out, _ = reg.Execute(t.Context(), call, "read_file", `{"path":"note.txt"}`)
	if out.IsError {
		t.Fatalf("read_file: %s", out.Content)
	}
	lines := strings.Split(out.Content, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 anchored lines, got %q", out.Content)
	}
	anchor2, _, _ := strings.Cut(lines[1], ":")

	editArgs, _ := json.Marshal(map[string]any{
		"path":         "note.txt",
		"start_anchor": anchor2,
		"new_content":  "BETA_UPDATED",
	})
	out, _ = reg.Execute(t.Context(), call, "edit_file", string(editArgs))
	if out.IsError {
		t.Fatalf("edit_file: %s", out.Content)
	}

	// Stale anchor must be rejected!
	out, _ = reg.Execute(t.Context(), call, "edit_file", string(editArgs))
	if !out.IsError || !strings.Contains(out.Content, "stale") {
		t.Fatalf("expected stale anchor rejection, got %+v", out)
	}

	// Directory listing, copy, move, remove
	_, _ = reg.Execute(t.Context(), call, "create_directory", `{"path":"sub"}`)
	out, _ = reg.Execute(t.Context(), call, "copy_path", `{"source":"note.txt","destination":"sub/copy.txt"}`)
	if out.IsError {
		t.Fatalf("copy_path: %s", out.Content)
	}
	out, _ = reg.Execute(t.Context(), call, "move_path", `{"source":"sub/copy.txt","destination":"sub/moved.txt"}`)
	if out.IsError {
		t.Fatalf("move_path: %s", out.Content)
	}
	out, _ = reg.Execute(t.Context(), call, "list_directory", `{"path":"sub"}`)
	if out.IsError || !strings.Contains(out.Content, "moved.txt") {
		t.Fatalf("list_directory: %+v", out)
	}
	out, _ = reg.Execute(t.Context(), call, "remove_path", `{"path":"sub","recursive":true}`)
	if out.IsError {
		t.Fatalf("remove_path: %s", out.Content)
	}

	// Escape & protected q internals must fail
	out, _ = reg.Execute(t.Context(), call, "read_file", `{"path":"../outside/secret.txt"}`)
	if !out.IsError {
		t.Fatalf("expected lexical escape rejection, got %+v", out)
	}
	out, _ = reg.Execute(t.Context(), call, "read_file", `{"path":"qdata/q.sqlite"}`)
	if !out.IsError || !strings.Contains(out.Content, "protected") {
		t.Fatalf("expected protected internal rejection, got %+v", out)
	}

	// Symlink escape if OS allows creating symlinks cleanly
	linkPath := filepath.Join(workspace, "escape-link")
	if err := os.Symlink(outside, linkPath); err == nil {
		out, _ = reg.Execute(t.Context(), call, "read_file", `{"path":"escape-link/secret.txt"}`)
		if !out.IsError || !strings.Contains(out.Content, "escapes") {
			t.Fatalf("expected symlink escape rejection, got %+v", out)
		}
	}
}

func TestShellLoomArchiveSkillsBrowserAndOrchestration(t *testing.T) {
	dir := t.TempDir()
	ctx := t.Context()

	rdb, err := sqlite.Open(ctx, filepath.Join(dir, "q.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()

	idx, err := blevehnsw.Open(filepath.Join(dir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	blobs, err := localfs.Open(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	defer blobs.Close()

	// Seed room, memory with observer isolation, and skill
	persona, _ := rdb.Personas().Create(ctx, domain.Persona{Name: "P1"})
	char, _ := rdb.Characters().Create(ctx, domain.Character{Name: "C1"})
	room, _ := rdb.Rooms().Create(ctx, domain.Room{Title: "R", Mode: domain.RoomModeDirect, PersonaID: persona.ID}, []domain.RoomMember{{
		CharacterID: char.ID, CharacterSnapshot: char, Active: true,
	}})
	_ = rdb.Memories().CreateMemory(ctx, domain.Memory{
		ID: "m-vis", RoomID: room.ID, OwnerID: char.ID, Kind: domain.MemoryKindEpisodic,
		Content: "Lantern password is amber.", ObservedBy: []string{char.ID}, OccurredAt: 1000, Status: domain.MemoryStatusActive, Revision: 1,
	})
	_ = rdb.Memories().CreateMemory(ctx, domain.Memory{
		ID: "m-hid", RoomID: room.ID, OwnerID: "other", Kind: domain.MemoryKindEpisodic,
		Content: "Secret lantern password for other actor.", ObservedBy: []string{"other"}, OccurredAt: 1000, Status: domain.MemoryStatusActive, Revision: 1,
	})
	_ = idx.Upsert(ctx, storage.SearchDocument{
		ID: "m-vis", EntityType: "memory", Scope: "room:" + room.ID, OwnerID: char.ID, ObservedBy: []string{char.ID},
		Text: "Lantern password is amber.", OccurredAt: 1000, Revision: 1,
	})
	_ = idx.Upsert(ctx, storage.SearchDocument{
		ID: "m-hid", EntityType: "memory", Scope: "room:" + room.ID, OwnerID: "other", ObservedBy: []string{"other"},
		Text: "Secret lantern password for other actor.", OccurredAt: 1000, Revision: 1,
	})

	skillsDir := filepath.Join(dir, "skills", "field-guide")
	_ = os.MkdirAll(skillsDir, 0o755)
	_ = os.WriteFile(filepath.Join(skillsDir, "SKILL.md"), []byte("---\nname: field-guide\ndescription: Lighthouse emergency procedure\n---\n# Guide\nCheck beacon."), 0o644)

	var browserCfg config.BrowserControlConfig
	browserCfg = config.BrowserControlConfig{
		Enabled:   true,
		Preferred: "cdp-q-driver",
		Drivers: map[string]config.BrowserDriverConfig{
			"cdp-q-driver": {Command: "mock-cdp", Args: []string{"cdp"}},
			"ego-browser":  {Command: "mock-ego", Args: []string{"--json"}},
		},
	}
	var lastBrowserCmd string
	var lastBrowserArgs []string
	browserCtrl := task.NewBrowserController(func() config.BrowserControlConfig { return browserCfg }, func(_ context.Context, cmd string, args []string) (string, error) {
		lastBrowserCmd = cmd
		lastBrowserArgs = args
		return "ok-browser", nil
	})

	cmds := task.NewCommandManager(10 * time.Second)
	defer cmds.Close()

	loom := task.NewLoomStore(rdb, blobs, 64<<20)
	skills := task.NewSkillStore(filepath.Join(dir, "skills"))
	reg := task.NewRegistry(task.RegistryDeps{
		RDB:      rdb,
		Search:   idx,
		Loom:     loom,
		Commands: cmds,
		Skills:   skills,
		Browser:  browserCtrl,
	})

	workDir := filepath.Join(dir, "work")
	_ = os.MkdirAll(workDir, 0o755)
	call := task.CallContext{
		RoomID:             room.ID,
		TurnID:             "t1",
		ActorID:            char.ID,
		ActorName:          char.Name,
		AllowedRoots:       []string{workDir},
		DataDir:            dir,
		DelegationDepth:    0,
		MaxDelegationDepth: 2,
		SubagentFn: func(_ context.Context, prompt, role string, depth int) (string, error) {
			return "subagent:" + role + ":" + prompt, nil
		},
	}

	// 1. Archive ObserverID isolation
	out, _ := reg.Execute(ctx, call, "search_archive", `{"query":"lantern password"}`)
	if !strings.Contains(out.Content, "m-vis") || strings.Contains(out.Content, "m-hid") {
		t.Fatalf("observer isolation failed in search_archive: %s", out.Content)
	}
	out, _ = reg.Execute(ctx, call, "get_archive_record", `{"entity_type":"memory","id":"m-hid"}`)
	if !out.IsError {
		t.Fatalf("expected observer error reading m-hid, got %s", out.Content)
	}

	// 2. Skills tools
	out, _ = reg.Execute(ctx, call, "search_skills", `{"query":"lighthouse"}`)
	if out.IsError || !strings.Contains(out.Content, "field-guide") {
		t.Fatalf("search_skills: %+v", out)
	}
	out, _ = reg.Execute(ctx, call, "get_skill", `{"name":"field-guide"}`)
	if out.IsError || !strings.Contains(out.Content, "Check beacon") {
		t.Fatalf("get_skill: %+v", out)
	}

	// 3. Large output Loom spill + loom_inspect + loom_read + loom_eval + timeout
	bigText := strings.TrimRight(strings.Repeat(`{"line":"hello-loom"}`+"\n", 1200), "\n") // > 24 KiB (> 16 KiB)
	_ = os.WriteFile(filepath.Join(workDir, "big.txt"), []byte(bigText), 0o644)
	out, _ = reg.Execute(ctx, call, "read_file", `{"path":"big.txt"}`)
	if out.IsError || out.ArtifactID == "" || !strings.Contains(out.Content, "loom://"+out.ArtifactID) {
		t.Fatalf("expected Loom spill, got %+v", out)
	}
	uri := "loom://" + out.ArtifactID
	outInspect, _ := reg.Execute(ctx, call, "loom_inspect", `{"id":"`+uri+`"}`)
	if outInspect.IsError || !strings.Contains(outInspect.Content, uri) {
		t.Fatalf("loom_inspect: %+v", outInspect)
	}
	outRead, _ := reg.Execute(ctx, call, "loom_read", `{"id":"`+uri+`","offset":0,"limit":64}`)
	if outRead.IsError || len(outRead.Content) == 0 {
		t.Fatalf("loom_read: %+v", outRead)
	}
	outEval, _ := reg.Execute(ctx, call, "loom_eval", `{"id":"`+uri+`","script":"content.split('\\n').filter(Boolean).length"}`)
	if outEval.IsError || outEval.Content != "1200" {
		t.Fatalf("loom_eval = %+v, want 1200", outEval)
	}
	outLoop, _ := reg.Execute(ctx, call, "loom_eval", `{"id":"`+uri+`","script":"while(true){}"}`)
	if !outLoop.IsError || !strings.Contains(outLoop.Content, "timeout") {
		t.Fatalf("expected loom_eval infinite loop timeout, got %+v", outLoop)
	}

	// 4. Async Shell
	shellCmd := "echo shell-ok"
	if goruntime.GOOS == "windows" {
		shellCmd = "Write-Output 'shell-ok'"
	}
	outCmd, _ := reg.Execute(ctx, call, "run_command", `{"command":`+jsonQuote(shellCmd)+`}`)
	if outCmd.IsError {
		t.Fatalf("run_command: %+v", outCmd)
	}
	var started struct {
		CommandID string `json:"command_id"`
	}
	_ = json.Unmarshal([]byte(outCmd.Content), &started)
	outWait, _ := reg.Execute(ctx, call, "wait", `{"command_id":"`+started.CommandID+`","seconds":10}`)
	if outWait.IsError || !strings.Contains(outWait.Content, "shell-ok") {
		t.Fatalf("wait output: %+v", outWait)
	}

	// 5. Browser controller switching (cdp-q-driver vs ego-browser)
	_, _ = reg.Execute(ctx, call, "browser_run", `{"args":["open","https://example.com"]}`)
	if lastBrowserCmd != "mock-cdp" || len(lastBrowserArgs) != 3 || lastBrowserArgs[0] != "cdp" {
		t.Fatalf("unexpected cdp browser dispatch: cmd=%q args=%v", lastBrowserCmd, lastBrowserArgs)
	}
	browserCfg.Preferred = "ego-browser"
	_, _ = reg.Execute(ctx, call, "browser_run", `{"args":["open","https://example.com"]}`)
	if lastBrowserCmd != "mock-ego" || lastBrowserArgs[0] != "--json" {
		t.Fatalf("unexpected ego-browser dispatch: cmd=%q args=%v", lastBrowserCmd, lastBrowserArgs)
	}

	// 6. Orchestration: delegate_task depth & ask_to_user pause
	outSub, _ := reg.Execute(ctx, call, "delegate_task", `{"prompt":"check docs","role":"Researcher"}`)
	if outSub.IsError || outSub.Content != "subagent:Researcher:check docs" {
		t.Fatalf("delegate_task: %+v", outSub)
	}
	deepCall := call
	deepCall.DelegationDepth = 2
	outDeep, _ := reg.Execute(ctx, deepCall, "delegate_task", `{"prompt":"too deep"}`)
	if !outDeep.IsError || !strings.Contains(outDeep.Content, "max_delegation_depth") {
		t.Fatalf("expected depth rejection, got %+v", outDeep)
	}

	outAsk, _ := reg.Execute(ctx, call, "ask_to_user", `{"prompt":"Proceed with cleanup?","choices":["Yes","No"]}`)
	if outAsk.PauseKind != "awaiting_user" || outAsk.UserQuestion == nil || len(outAsk.UserQuestion.Choices) != 2 {
		t.Fatalf("ask_to_user output: %+v", outAsk)
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
