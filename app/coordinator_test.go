package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/gitwork"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/workspacememory"
)

func TestWorkSearchFallsBackWhenEmbeddingProviderCannotStart(t *testing.T) {
	host, c, project, _ := testManagerHost(t)
	value, err := host.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	value.Embedding = config.EmbeddingConfig{Model: "fixture-embedding", Dimensions: 3}
	if err := host.store.Save(value); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateItem(t.Context(), workitems.CreateInput{WorkspaceID: project.ID, Kind: workitems.Issue, Title: "Fallback search", Content: "needle searchable evidence", Author: "user"}); err != nil {
		t.Fatal(err)
	}
	host.factory = func(config.Config) (chatClient, error) { return nil, errors.New("fixture embedding unavailable") }
	in := library.WorkSearchRequest{WorkspaceID: project.ID, Query: "needle", Mode: "hybrid"}
	out, err := host.SearchWork(t.Context(), in)
	if err != nil || len(out.Hits) == 0 || out.Fallback == "" {
		t.Fatalf("hybrid fallback: %+v %v", out, err)
	}
	in.Mode = "semantic"
	if _, err := host.SearchWork(t.Context(), in); !errors.Is(err, library.ErrSemanticUnavailable) {
		t.Fatalf("semantic failure concealed: %v", err)
	}
}

func TestTicketNativeWorkerFeedbackAndPinnedMerge(t *testing.T) {
	host, c, project, _ := testManagerHost(t)
	if err := (workspacememory.ConfigStore{Dir: host.store.Dir}).Save(workspacememory.Config{Host: "127.0.0.1", Port: reserveTestPort(t)}); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	root := project.WorkspaceRoots[0]
	delegationGit(t, root, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	delegationGit(t, root, "add", "README.md")
	delegationGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "initial")
	// A manager/tool may create metadata after the base was initialized.
	if err := os.MkdirAll(filepath.Join(root, ".q", "loom"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".q", "loom", "evidence.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := host.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	value.Provider.Model = "plan-model"
	if err := host.store.Save(value); err != nil {
		t.Fatal(err)
	}
	configured := &planningClient{}
	host.factory = func(config.Config) (chatClient, error) { return configured, nil }
	m, err := c.PutMember(ctx, project.ID, workitems.MemberInput{AgentID: subagent.BuiltinSeniorDeveloperID, DefinitionRoot: root, ExecutionRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	item, err := c.CreateItem(ctx, workitems.CreateInput{WorkspaceID: project.ID, Kind: workitems.Ticket, Title: "Write note", Content: "Create child.txt", Acceptance: []string{"Only the reviewed content is merged"}, ExecutionMode: "git", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	var previous workitems.Attempt
	for index, content := range []string{"first", "revised"} {
		if index == 0 {
			item, err = c.ControlTicket(ctx, item.ID, workitems.TicketAction{Action: "queue", Revision: item.Revision})
			if err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := c.ReviewAttempt(ctx, previous.ID, workitems.ReviewInput{ManagerSessionID: project.ManagerSessionID, Decision: "request_changes", Feedback: "Revise the note", RequestID: "feedback"}); err != nil {
				t.Fatal(err)
			}
			item, err = c.GetItem(ctx, item.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
		candidates, err := host.coordinatorCandidates(ctx, project, item, []workitems.Member{m})
		if err != nil || len(candidates) != 1 {
			t.Fatalf("candidates %+v %v", candidates, err)
		}
		candidate := candidates[0]
		a, err := c.ClaimTicket(ctx, item.ID, workitems.ClaimInput{Revision: item.Revision, MemberID: m.ID, MemberRevision: m.Revision, DefinitionDigest: candidate.Digest, Owner: "test", RequestID: content})
		if err != nil {
			t.Fatal(err)
		}
		if index > 0 && a.WorkerSessionID != previous.WorkerSessionID {
			t.Fatal("feedback changed worker session")
		}
		a, err = c.UpdateAttempt(ctx, a.ID, workitems.AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: "running"})
		if err != nil {
			t.Fatal(err)
		}
		configured.responses = []client.Message{{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall("task_start", `{"objective":"write note"}`)}}, {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall("write_file", `{"path":"child.txt","content":"`+content+`"}`)}}, {Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall("task_complete", `{"outcome":"succeeded","summary":"Write note"}`)}}}
		result, err := host.runTicket(ctx, c, project, item, candidate, a, "Create child.txt: "+content)
		if err != nil || result.Outcome != "succeeded" || result.ChangeRequest == nil {
			t.Fatalf("worker: %+v %v", result, err)
		}
		if _, err := os.Stat(filepath.Join(root, "child.txt")); !os.IsNotExist(err) {
			t.Fatalf("worker edited parent before review: %v", err)
		}
		if index > 0 && result.ChangeRequest.HeadCommit == previous.ChangeRequest.HeadCommit {
			t.Fatal("feedback reused old submitted head")
		}
		workerPage, err := c.ReadWorker(ctx, a.ID, "", 1)
		if err != nil || workerPage.SessionID != a.WorkerSessionID || len(workerPage.Messages) != 1 || workerPage.NextCursor == "" {
			t.Fatalf("worker read %+v %v", workerPage, err)
		}
		nextPage, err := c.ReadWorker(ctx, a.ID, workerPage.NextCursor, 1)
		if err != nil || len(nextPage.Messages) != 1 || nextPage.Messages[0].Index != 1 {
			t.Fatalf("worker page %+v %v", nextPage, err)
		}
		a, err = c.UpdateAttempt(ctx, a.ID, workitems.AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: "awaiting_review", Result: []byte(`{"outcome":"succeeded"}`), ChangeRequest: result.ChangeRequest})
		if err != nil {
			t.Fatal(err)
		}
		previous = a
	}
	manager := gitwork.Manager{Root: filepath.Join(host.store.Dir, "worktrees")}
	diff, err := manager.Read(ctx, *previous.ChangeRequest)
	if err != nil || !strings.Contains(diff.Patch, "revised") {
		t.Fatalf("review diff: %+v %v", diff, err)
	}
	if _, err := c.ReviewAttempt(ctx, previous.ID, workitems.ReviewInput{ManagerSessionID: project.ManagerSessionID, Decision: "accept", RequestID: "merge"}); err != nil {
		t.Fatal(err)
	}
	a, err := c.ClaimMerge(ctx, previous.ID, "merge-owner")
	if err != nil {
		t.Fatal(err)
	}
	request, err := manager.Merge(ctx, root, *a.ChangeRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateAttempt(ctx, a.ID, workitems.AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: "accepted", ChangeRequest: &request}); err != nil {
		t.Fatal(err)
	}
	item, err = c.GetItem(context.Background(), item.ID)
	if err != nil || item.State != "done" {
		t.Fatalf("final ticket %+v %v", item, err)
	}
	body, err := os.ReadFile(filepath.Join(root, "child.txt"))
	if err != nil || string(body) != "revised" {
		t.Fatalf("merged contents %q %v", body, err)
	}
}

func TestTicketACPWorkerResumesSameRemoteSessionAndBlocksUnknownPrompt(t *testing.T) {
	host, c, project, _ := testManagerHost(t)
	ctx := t.Context()
	root := project.WorkspaceRoots[0]
	profiles := (model{hostState: hostState{store: host.store}}).customStoreAt(root)
	if err := profiles.Save(subagent.Profile{Version: 1, Name: "acp-reader", Kind: subagent.AgentKindExternal, Agent: "wire", SystemPrompt: "Return evidence."}, "workspace", nil); err != nil {
		t.Fatal(err)
	}
	m, err := c.PutMember(ctx, project.ID, workitems.MemberInput{AgentID: "workspace/acp-reader", DefinitionRoot: root, ExecutionRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	item, err := c.CreateItem(ctx, workitems.CreateInput{WorkspaceID: project.ID, Kind: workitems.Ticket, Title: "ACP evidence", Content: "Inspect", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	var previous workitems.Attempt
	for index := 0; index < 2; index++ {
		if index == 0 {
			item, err = c.ControlTicket(ctx, item.ID, workitems.TicketAction{Action: "queue", Revision: item.Revision})
		} else {
			_, err = c.ReviewAttempt(ctx, previous.ID, workitems.ReviewInput{ManagerSessionID: project.ManagerSessionID, Decision: "request_changes", Feedback: "Continue", RequestID: "feedback"})
			if err == nil {
				item, err = c.GetItem(ctx, item.ID)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		candidates, err := host.coordinatorCandidates(ctx, project, item, []workitems.Member{m})
		if err != nil || len(candidates) != 1 {
			t.Fatalf("ACP candidate %+v %v", candidates, err)
		}
		candidate := candidates[0]
		a, err := c.ClaimTicket(ctx, item.ID, workitems.ClaimInput{Revision: item.Revision, MemberID: m.ID, MemberRevision: m.Revision, DefinitionDigest: candidate.Digest, Owner: "test", RequestID: []string{"first", "second"}[index]})
		if err != nil {
			t.Fatal(err)
		}
		a, err = c.UpdateAttempt(ctx, a.ID, workitems.AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: "running"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := host.runTicket(ctx, c, project, item, candidate, a, "Inspect evidence")
		expected := "resumed=false"
		if index > 0 {
			expected = "resumed=true"
		}
		if err != nil || !strings.Contains(result.Report, expected) {
			t.Fatalf("ACP worker %+v %v", result, err)
		}
		a, err = c.UpdateAttempt(ctx, a.ID, workitems.AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: "awaiting_review", Result: []byte(`{"outcome":"succeeded"}`)})
		if err != nil {
			t.Fatal(err)
		}
		if index > 0 && a.WorkerSessionID != previous.WorkerSessionID {
			t.Fatal("ACP feedback replaced worker")
		}
		previous = a
	}
	worker := ticketWorkerStore(host.store.Dir, previous)
	path := filepath.Join(worker.SessionDir(), "ticket-acp.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved ticketACPCheckpoint
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	saved.Completed = false
	saved.Started = true
	raw, _ = json.Marshal(saved)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	candidates, err := host.coordinatorCandidates(ctx, project, item, []workitems.Member{m})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.runACPTicket(ctx, c, worker, candidates[0], previous, "Do not replay", root); err == nil || !strings.Contains(err.Error(), "unknown outcome") {
		t.Fatalf("unknown ACP prompt replayed: %v", err)
	}
}
