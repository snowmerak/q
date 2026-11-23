package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/gitwork"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/workspace"
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

func TestJoinedJuniorDeveloperIsAssignedThroughTickets(t *testing.T) {
	host, c, project, _ := testManagerHost(t)
	root := project.WorkspaceRoots[0]
	member, err := c.PutMember(t.Context(), project.ID, workitems.MemberInput{AgentID: subagent.BuiltinJuniorDeveloperID, DefinitionRoot: root, ExecutionRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := host.coordinatorCandidates(t.Context(), project, workitems.Item{ExecutionMode: "git"}, []workitems.Member{member})
	if err != nil || len(candidates) != 1 || candidates[0].Member.ID != member.ID {
		t.Fatalf("joined junior not assignable: %+v %v", candidates, err)
	}
	candidates, err = host.coordinatorCandidates(t.Context(), project, workitems.Item{ExecutionMode: "report"}, []workitems.Member{member})
	if err != nil || len(candidates) != 0 {
		t.Fatalf("writing worker admitted to read-only report: %+v %v", candidates, err)
	}
}

func TestBuiltinCodeReviewerRunsReportTicketReadOnly(t *testing.T) {
	host, c, project, _ := testManagerHost(t)
	ctx := t.Context()
	root := project.WorkspaceRoots[0]
	if err := (workspacememory.ConfigStore{Dir: host.store.Dir}).Save(workspacememory.Config{Host: "127.0.0.1", Port: reserveTestPort(t)}); err != nil {
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
	var members []workitems.Member
	for _, id := range []string{subagent.BuiltinSeniorDeveloperID, subagent.BuiltinJuniorDeveloperID, subagent.BuiltinCodeReviewerID} {
		member, err := c.PutMember(ctx, project.ID, workitems.MemberInput{AgentID: id, DefinitionRoot: root, ExecutionRoot: root})
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, member)
	}
	item, err := c.CreateItem(ctx, workitems.CreateInput{
		WorkspaceID: project.ID, Kind: workitems.Ticket, Title: "Review a note", Content: "Inspect notes.txt",
		Author: "user", ExecutionMode: "report", RequestedRole: config.AgentRoleReviewer,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := host.coordinatorCandidates(ctx, project, item, members)
	if err != nil || len(candidates) != 1 || candidates[0].Member.AgentID != subagent.BuiltinCodeReviewerID {
		t.Fatalf("report candidates: %+v %v", candidates, err)
	}
	candidate := candidates[0]
	attempt, err := c.ClaimTicket(ctx, item.ID, workitems.ClaimInput{
		Revision: item.Revision, MemberID: candidate.Member.ID, MemberRevision: candidate.Member.Revision,
		Role: candidate.Definition.Info.Role, DefinitionDigest: candidate.Digest, Owner: "test", RequestID: "review",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("Report evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	issue, err := c.CreateItem(ctx, workitems.CreateInput{WorkspaceID: project.ID, Kind: workitems.Issue, Title: "Review follow-up", Content: "Track review findings", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	configured := &planningClient{responses: []client.Message{
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall("task_start", `{"objective":"Review notes"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall("read_file", `{"path":"notes.txt"}`)}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall("issue_reply", fmt.Sprintf(`{"id":%q,"content":"The note needs supporting evidence."}`, issue.ID))}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall("post_update", fmt.Sprintf(`{"id":%q,"revision":1,"content":"Inspect notes.txt and confirm sources"}`, item.BodyPostID))}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall("ticket_create", fmt.Sprintf(`{"title":"Find supporting evidence","content":"Inspect the sources.","execution_mode":"report","linked_issue_id":%q}`, issue.ID))}},
		{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall("task_complete", `{"outcome":"succeeded","summary":"Reviewed the note","findings":["The note needs supporting evidence."]}`)}},
	}}
	host.factory = func(config.Config) (chatClient, error) { return configured, nil }
	result, err := host.runTicket(ctx, c, project, item, candidate, attempt, "Review notes.txt")
	if err != nil || result.Outcome != "succeeded" || result.ChangeRequest != nil || len(result.Findings) != 1 {
		t.Fatalf("review result: %+v %v", result, err)
	}
	readEvidence := false
	for _, request := range configured.requests {
		readEvidence = readEvidence || slices.ContainsFunc(request.Messages, func(message client.Message) bool {
			return message.Role == client.RoleTool && message.Name == "read_file" && strings.Contains(message.TextContent(), "Report evidence")
		})
		if slices.ContainsFunc(request.Tools, func(tool client.Tool) bool {
			switch tool.Function.Name {
			case "write_file", "edit_file", "run_command", "ticket_review":
				return true
			default:
				return false
			}
		}) {
			t.Fatal("report reviewer received a mutation tool")
		}
	}
	if !readEvidence {
		t.Fatal("report reviewer did not read the project file")
	}
	thread, err := c.ReadThread(ctx, issue.ID, "", 10)
	if err != nil || len(thread.Posts) != 2 || thread.Posts[1].Author != "agent/"+attempt.WorkerSessionID {
		t.Fatalf("reviewer's issue reply: %+v %v", thread, err)
	}
	body, err := c.ReadContent(ctx, item.BodyPostID, "", 100)
	if err != nil || body.Post.Content != "Inspect notes.txt and confirm sources" || body.Post.Author != "user" {
		t.Fatalf("assigned ticket body: %+v %v", body, err)
	}
	tickets, err := c.ListItems(ctx, project.ID, workitems.Ticket, "", 10)
	if err != nil || !slices.ContainsFunc(tickets.Items, func(ticket workitems.Item) bool {
		return ticket.Title == "Find supporting evidence" && ticket.LinkedIssueID == issue.ID && ticket.ExecutionState == "queued"
	}) {
		t.Fatalf("reviewer's follow-up ticket: %+v %v", tickets, err)
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
	value.Agents.Roles = map[string]config.AgentConfig{config.AgentRoleReviewer: {Model: "legacy-role-model"}}
	value.Agents.SubagentTargets = map[string]config.SubagentTarget{subagent.BuiltinSeniorDeveloperID: {Model: "global-model"}}
	if err := host.store.Save(value); err != nil {
		t.Fatal(err)
	}
	if err := (workspace.Store{Root: root}).SaveSubagentTargets(map[string]config.SubagentTarget{
		subagent.BuiltinSeniorDeveloperID: {Model: "plan-model"},
	}); err != nil {
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
	dependent, err := c.CreateItem(ctx, workitems.CreateInput{WorkspaceID: project.ID, Kind: workitems.Ticket, Title: "Use merged note", Content: "Inspect child.txt after the merge", Author: "user", DependsOn: []string{item.ID}})
	if err != nil {
		t.Fatal(err)
	}
	assertDependentWaiting := func() {
		t.Helper()
		current, err := c.GetItem(ctx, dependent.ID)
		if err != nil || len(current.WaitingOn) != 1 || current.WaitingOn[0] != item.ID || current.ActiveAttemptID != "" {
			t.Fatalf("dependent started before the source recorded a successful merge: %+v %v", current, err)
		}
	}
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
		if index == 0 {
			page, err := c.ReadLatestWorker(ctx, a.ID, 2)
			if err != nil || page.SessionID != a.WorkerSessionID || len(page.Messages) != 0 {
				t.Fatalf("worker before the first checkpoint: %+v %v", page, err)
			}
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
		if len(configured.requests) == 0 || configured.requests[len(configured.requests)-1].Model != "plan-model" {
			t.Fatal("ticket worker did not use the directly assigned API model")
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
		tail, err := c.ReadLatestWorker(ctx, a.ID, 1)
		if err != nil || tail.RunID != a.ID || len(tail.Messages) != 1 || tail.Messages[0].Index <= nextPage.Messages[0].Index || tail.NextCursor != "" {
			t.Fatalf("worker latest checkpoint: %+v %v", tail, err)
		}
		recent, err := c.ReadLatestWorker(ctx, a.ID, 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range recent.Messages {
			if message.Role == string(client.RoleSystem) || message.Role == string(client.RoleDeveloper) {
				t.Fatalf("worker activity included an instruction message at index %d", message.Index)
			}
		}
		a, err = c.UpdateAttempt(ctx, a.ID, workitems.AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: "awaiting_review", Result: []byte(`{"outcome":"succeeded"}`), ChangeRequest: result.ChangeRequest})
		if err != nil {
			t.Fatal(err)
		}
		previous = a
		assertDependentWaiting()
	}
	manager := gitwork.Manager{Root: filepath.Join(host.store.Dir, "worktrees")}
	diff, err := manager.Read(ctx, *previous.ChangeRequest)
	if err != nil || !strings.Contains(diff.Patch, "revised") {
		t.Fatalf("review diff: %+v %v", diff, err)
	}
	if _, err := c.ReviewAttempt(ctx, previous.ID, workitems.ReviewInput{ManagerSessionID: project.ManagerSessionID, Decision: "accept", RequestID: "merge"}); err != nil {
		t.Fatal(err)
	}
	assertDependentWaiting()
	a, err := c.ClaimMerge(ctx, previous.ID, "merge-owner")
	if err != nil {
		t.Fatal(err)
	}
	request, err := manager.Merge(ctx, root, *a.ChangeRequest)
	if err != nil {
		t.Fatal(err)
	}
	assertDependentWaiting()
	if _, err := c.UpdateAttempt(ctx, a.ID, workitems.AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: "accepted", ChangeRequest: &request}); err != nil {
		t.Fatal(err)
	}
	item, err = c.GetItem(context.Background(), item.ID)
	if err != nil || item.State != "done" {
		t.Fatalf("final ticket %+v %v", item, err)
	}
	ready, err := c.ListTicketQueue(ctx, project.ID, "assignment", "", 100)
	if err != nil || len(ready.Items) != 1 || ready.Items[0].ID != dependent.ID {
		t.Fatalf("successful merge did not release dependent: %+v %v", ready, err)
	}
	body, err := os.ReadFile(filepath.Join(root, "child.txt"))
	if err != nil || string(body) != "revised" {
		t.Fatalf("merged contents %q %v", body, err)
	}
}

func TestTicketACPWorkerResumesSameRemoteSessionAndBlocksUnknownPrompt(t *testing.T) {
	fast := config.ACPSelection{AgentModel: "fast", AgentReasoningEffort: "low"}
	deep := config.ACPSelection{AgentModel: "deep", AgentReasoningEffort: "max"}
	t.Run("profile target", func(t *testing.T) {
		testTicketACPWorkerModelSelection(t, subagent.Profile{
			Version: 1, Name: "acp-reader", Kind: subagent.AgentKindInner, Role: config.AgentRoleResearch,
			Target: &config.SubagentTarget{Agent: "wire", ACPSelection: deep},
			Tools:  []string{"read_file"}, SystemPrompt: "Return evidence.",
		}, "model=deep effort=max")
	})
	t.Run("external profile", func(t *testing.T) {
		testTicketACPWorkerModelSelection(t, subagent.Profile{
			Version: 1, Name: "acp-reader", Kind: subagent.AgentKindExternal, Agent: "wire",
			ACPSelection: fast, SystemPrompt: "Return evidence.",
		}, "model=fast effort=low")
	})
}

func testTicketACPWorkerModelSelection(t *testing.T, profile subagent.Profile, expectedSettings string) {
	t.Helper()
	host, c, project, _ := testManagerHost(t)
	ctx := t.Context()
	root := project.WorkspaceRoots[0]
	profiles := (model{hostState: hostState{store: host.store}}).customStoreAt(root)
	if err := profiles.Save(profile, "workspace", nil); err != nil {
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
		if err != nil || !strings.Contains(result.Report, expected) || !strings.Contains(result.Report, expectedSettings) ||
			!strings.Contains(result.Report, "skills=true work-tools=true skill-guidance=true") {
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
