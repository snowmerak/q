package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/change"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/gitwork"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/providerhost"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/workspacememory"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == providerhost.ChildCommand && os.Args[2] == "--config" {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			cancel()
		}()
		key := os.Getenv(providerhost.ChildAPIKeyEnv)
		_ = os.Unsetenv(providerhost.ChildAPIKeyEnv)
		err := providerhost.RunChild(ctx, os.Args[3], key, providerhost.EncodeReady(json.NewEncoder(os.Stdout)))
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func TestTicketChildReviewResumesNativeWorkerAndMergesIntoParent(t *testing.T) {
	host, lib, project, integrator := testManagerHost(t)
	ctx := t.Context()
	root := project.WorkspaceRoots[0]
	delegationGit(t, root, "init", "-b", "main")
	delegationGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "initial")
	if err := (workspacememory.ConfigStore{Dir: host.store.Dir}).Save(workspacememory.Config{Host: "127.0.0.1", Port: reserveTestPort(t)}); err != nil {
		t.Fatal(err)
	}
	value, err := host.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	value.Provider.Model = "plan-model"
	value.Agents.SubagentTargets = map[string]config.SubagentTarget{
		subagent.BuiltinSeniorDeveloperID: {Model: "plan-model"},
		subagent.BuiltinJuniorDeveloperID: {Model: "plan-model"},
	}
	if err := host.store.Save(value); err != nil {
		t.Fatal(err)
	}
	configured := &planningClient{}
	host.factory = func(config.Config) (chatClient, error) { return configured, nil }
	join := func(agent string) workitems.Member {
		t.Helper()
		member, err := lib.PutMember(ctx, project.ID, workitems.MemberInput{AgentID: agent, DefinitionRoot: root, ExecutionRoot: root})
		if err != nil {
			t.Fatal(err)
		}
		return member
	}
	senior, junior := join(subagent.BuiltinSeniorDeveloperID), join(subagent.BuiltinJuniorDeveloperID)
	parent, err := lib.CreateItem(ctx, workitems.CreateInput{WorkspaceID: project.ID, Kind: workitems.Ticket, Title: "Parent", Content: "Delegate implementation", Author: "user", ExecutionMode: "git", RequestedMemberID: senior.ID})
	if err != nil {
		t.Fatal(err)
	}
	call := func(name, args string) client.Message {
		return client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{planToolCall(name, args)}}
	}
	run := func(id string, member workitems.Member, turn string, status string, steps ...client.Message) workitems.Attempt {
		t.Helper()
		item, err := lib.GetItem(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		selection, err := host.coordinatorCandidates(ctx, project, item, []workitems.Member{member})
		if err != nil || len(selection) != 1 {
			t.Fatalf("candidates: %+v %v", selection, err)
		}
		a, err := lib.ClaimTicket(ctx, id, workitems.ClaimInput{Revision: item.Revision, MemberID: member.ID, MemberRevision: member.Revision, Role: selection[0].Definition.Info.Role, Owner: "test", DefinitionDigest: selection[0].Digest, RequestID: turn, ParallelLimit: 1})
		if err != nil {
			t.Fatal(err)
		}
		configured.responses = append([]client.Message{call("task_start", `{"objective":"Work on the assigned task"}`)}, steps...)
		configured.responses = append(configured.responses, call("task_complete", `{"outcome":"succeeded","summary":"Turn complete"}`))
		result, err := host.runTicket(ctx, lib, project, item, selection[0], a, turn)
		if err != nil || result.ChangeRequest == nil || (result.Outcome == workitems.WaitingChildren) != (status == workitems.WaitingChildren) {
			t.Fatalf("%s: %+v %v", turn, result, err)
		}
		raw, _ := json.Marshal(result)
		a, err = lib.UpdateAttempt(ctx, a.ID, workitems.AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: status, Result: raw, ChangeRequest: result.ChangeRequest})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	issued := run(parent.ID, senior, "delegate", workitems.WaitingChildren, call("ticket_create", `{"title":"Child","content":"Create nested.txt","execution_mode":"git","as_child":true,"request_id":"create-child","requested_role":"coder"}`))
	children, err := lib.ListChildren(ctx, parent.ID, issued.WorktreeID)
	if err != nil || len(children.Items) != 1 {
		t.Fatalf("children: %+v %v", children, err)
	}
	childItem := children.Items[0]
	if childItem.RequestedRole != config.AgentRoleCoder || childItem.RequestedMemberID != "" {
		t.Fatalf("unexpected child routing: %+v", childItem)
	}
	candidates, err := host.coordinatorCandidates(ctx, project, childItem, []workitems.Member{senior, junior})
	if err != nil || len(candidates) != 1 || candidates[0].Member.ID != junior.ID {
		t.Fatalf("role-filtered candidates: %+v %v", candidates, err)
	}
	var responseError *library.ResponseError
	if _, err := lib.ClaimTicket(ctx, childItem.ID, workitems.ClaimInput{Revision: childItem.Revision, MemberID: junior.ID, MemberRevision: junior.Revision, Role: config.AgentRoleReviewer, Owner: "test", DefinitionDigest: candidates[0].Digest, RequestID: "wrong-role", ParallelLimit: 1}); !errors.As(err, &responseError) || responseError.StatusCode != http.StatusConflict {
		t.Fatalf("reservation accepted a mismatched role: %v", err)
	}
	child := run(childItem.ID, junior, "implement", "awaiting_review", call("write_file", `{"path":"nested.txt","content":"nested"}`))
	if child.MemberID != junior.ID || child.WorkerSessionID == "" {
		t.Fatalf("child assignment not recorded: %+v", child)
	}
	if child.ChangeRequest.BaseRef != issued.ChangeRequest.HeadRef || child.ChangeRequest.RepositoryRoot != issued.ChangeRequest.WorktreePath {
		t.Fatalf("child targeted the wrong checkout: %+v", child.ChangeRequest)
	}
	if _, err := lib.ReviewAttempt(ctx, child.ID, workitems.ReviewInput{ManagerSessionID: project.ManagerSessionID, Decision: "accept", RequestID: "early"}); !errors.As(err, &responseError) || responseError.StatusCode != http.StatusConflict {
		t.Fatalf("Integrator accepted a child: %v", err)
	}
	resume, err := lib.ListTicketQueue(ctx, project.ID, "resume", "", 10)
	if err != nil || len(resume.Items) != 1 || resume.Items[0].ID != parent.ID {
		t.Fatalf("resume queue: %+v %v", resume, err)
	}
	reviewed := run(parent.ID, senior, "review", workitems.WaitingChildren,
		call("ticket_attempt_get", `{}`), call("ticket_change_read", `{}`),
		call("ticket_worker_read", `{"limit":1}`), call("ticket_review", `{"decision":"accept"}`))
	if reviewed.WorkerSessionID != issued.WorkerSessionID || reviewed.WorktreeID != issued.WorktreeID {
		t.Fatal("child review opened a different parent session or worktree")
	}
	manager := gitwork.Manager{Root: filepath.Join(host.store.Dir, "worktrees")}
	merge := func(id string) change.Request {
		t.Helper()
		a, err := lib.ClaimMerge(ctx, id, "merge")
		if err != nil {
			t.Fatal(err)
		}
		pinned, err := manager.PrepareCandidate(ctx, *a.ChangeRequest)
		if err != nil {
			t.Fatal(err)
		}
		request, err := manager.ApplyCandidate(ctx, *a.ChangeRequest, pinned)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lib.UpdateAttempt(ctx, a.ID, workitems.AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: "accepted", ChangeRequest: &request}); err != nil {
			t.Fatal(err)
		}
		return request
	}
	merge(child.ID)
	if _, err := os.Stat(filepath.Join(root, "nested.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child change reached main early: %v", err)
	}
	final := run(parent.ID, senior, "submit combined", "awaiting_review")
	if final.WorkerSessionID != issued.WorkerSessionID || final.ReviewChildAttemptID != "" {
		t.Fatalf("invalid final continuation: %+v", final)
	}
	if err := host.ApplyGateway(ctx, gateway.Config{Providers: []gateway.ProviderConfig{{
		ID: "fixture", Type: "openai-compatible", Enabled: true, BaseURL: "http://127.0.0.1:1/v1", Models: []string{"plan-model"},
	}}}); err != nil {
		t.Fatal(err)
	}
	configured.responses = []client.Message{
		call("ticket_attempt_get", `{}`),
		call("ticket_review", `{"decision":"accept"}`),
		{Role: client.RoleAssistant, Content: "Approved for merge."},
	}
	entry := workitems.InboxEntry{TicketID: parent.ID, OperationID: "review-parent"}
	if err := host.deliverTicketReview(ctx, project, entry, final); err != nil {
		t.Fatal(err)
	}
	approved, err := lib.GetAttempt(ctx, final.ID)
	if err != nil || approved.Status != "merge_queued" || !strings.HasPrefix(approved.DecisionID, "assigned-review-") {
		t.Fatalf("Integrator review: %+v %v", approved, err)
	}
	if err := host.deliverTicketReview(ctx, project, entry, final); err != nil {
		t.Fatalf("replaying the same review operation: %v", err)
	}
	configured.responses = []client.Message{{Role: client.RoleAssistant, Content: "Ready."}}
	if err := host.Run(ctx, integrator, integrator.SessionID, "Continue as Integrator.", nil); err != nil {
		t.Fatal(err)
	}
	live := host.liveSessions[liveSessionKey(integrator, integrator.SessionID)]
	if live.prepared.review.Active() {
		t.Fatal("the assigned review leaked into the ordinary Integrator turn")
	}
	merge(final.ID)
	if data, err := os.ReadFile(filepath.Join(root, "nested.txt")); err != nil || string(data) != "nested" {
		t.Fatalf("main file: %q %v", data, err)
	}
}
