package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/coordinator"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/workspacememory"
)

func claimInteractionTicket(t *testing.T, host *SessionHost, c *library.Client, project workitems.Workspace, agentID string) (workitems.Item, coordinator.Candidate, workitems.Attempt) {
	t.Helper()
	ctx := t.Context()
	root := project.WorkspaceRoots[0]
	member, err := c.PutMember(ctx, project.ID, workitems.MemberInput{AgentID: agentID, DefinitionRoot: root, ExecutionRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	item, err := c.CreateItem(ctx, workitems.CreateInput{
		WorkspaceID: project.ID, Kind: workitems.Ticket, Title: "Inspect the notes",
		Content: "Report what is in the notes.", Author: "user", ExecutionMode: "report",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := host.coordinatorCandidates(ctx, project, item, []workitems.Member{member})
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%v err=%v", candidates, err)
	}
	candidate := candidates[0]
	a, err := c.ClaimTicket(ctx, item.ID, workitems.ClaimInput{
		Revision: item.Revision, MemberID: member.ID, MemberRevision: member.Revision,
		Owner: "test", DefinitionDigest: candidate.Digest, RequestID: "claim-interaction",
	})
	if err != nil {
		t.Fatal(err)
	}
	return item, candidate, a
}

func TestNativeTicketAppliesGuidanceBeforeUnstartedToolsAndSealsSubmission(t *testing.T) {
	host, c, project, _ := testManagerHost(t)
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
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
	item, candidate, a := claimInteractionTicket(t, host, c, project, subagent.BuiltinCodeReviewerID)
	issue, err := c.CreateItem(ctx, workitems.CreateInput{WorkspaceID: project.ID, Kind: workitems.Issue, Title: "An issue", Content: "Original issue", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	round := 0
	ready, release := make(chan struct{}), make(chan struct{})
	in := workitems.TicketGuidanceInput{Content: "Keep the findings private.", RequestID: "guidance-once"}
	configured := &planningClient{respond: func(ctx context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
		round++
		var call client.ToolCall
		switch round {
		case 1:
			call = planToolCall("task_start", `{"objective":"Inspect notes"}`)
		case 2:
			close(ready)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			call = planToolCall("issue_reply", fmt.Sprintf(`{"id":%q,"content":"An outdated response"}`, issue.ID))
		case 3:
			last := request.Messages[len(request.Messages)-1]
			if last.Role != client.RoleUser || last.Content != in.Content ||
				!slices.ContainsFunc(request.Messages, func(message client.Message) bool {
					return message.Role == client.RoleTool && message.Name == "issue_reply" && strings.Contains(message.Content, "new user guidance")
				}) {
				return nil, errors.New("guidance or the skipped tool result is missing from the next model request")
			}
			call = planToolCall("task_complete", `{"outcome":"succeeded","summary":"Private findings"}`)
		default:
			return nil, errors.New("unexpected model round")
		}
		return &client.ChatResponse{Choices: []client.Choice{{Message: client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{call}}}}}, nil
	}}
	host.factory = func(config.Config) (chatClient, error) { return configured, nil }
	finished := make(chan error, 1)
	go func() {
		result, err := host.runTicket(ctx, c, project, item, candidate, a, "Inspect notes")
		if err == nil && result.Outcome != "succeeded" {
			err = fmt.Errorf("unexpected result %+v", result)
		}
		finished <- err
	}()
	select {
	case <-ready:
	case err := <-finished:
		t.Fatalf("worker ended before guidance: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	guidance, err := c.QueueTicketGuidance(ctx, a.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	seal, err := c.UpdateAttempt(ctx, a.ID, workitems.AttemptUpdate{
		Owner: a.Owner, Token: a.Token, Generation: a.Generation, SealGuidance: true,
	})
	if err != nil || !seal.AcceptsGuidance() || seal.Guidance == nil || seal.Guidance.ID != guidance.ID {
		t.Fatalf("submission overtook a queued request: %+v, %v", seal, err)
	}
	page, err := c.ReadLatestWorker(ctx, a.ID, 20)
	if err != nil || !page.AcceptsGuidance || page.PendingGuidance == nil || page.PendingGuidance.ID != guidance.ID {
		t.Fatalf("pending guidance: %+v, %v", page, err)
	}
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	replayed, err := c.QueueTicketGuidance(ctx, a.ID, in)
	if err != nil || replayed != guidance {
		t.Fatalf("request retry: %+v, %v", replayed, err)
	}
	_, err = c.QueueTicketGuidance(ctx, a.ID, workitems.TicketGuidanceInput{Content: "Too late.", RequestID: "after-seal"})
	var response *library.ResponseError
	if !errors.As(err, &response) || response.StatusCode != http.StatusConflict {
		t.Fatalf("submitted worker accepted new guidance: %v", err)
	}
	page, err = c.ReadLatestWorker(ctx, a.ID, 20)
	if err != nil || page.PendingGuidance != nil || page.AcceptsGuidance {
		t.Fatalf("after completion: %+v, %v", page, err)
	}
	applied := 0
	for _, message := range page.Messages {
		if message.Name == workitems.TicketGuidanceMessagePrefix+guidance.ID && message.Content == in.Content {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("guidance appeared %d times", applied)
	}
	thread, err := c.ReadThread(ctx, issue.ID, "", 10)
	if err != nil || len(thread.Posts) != 1 {
		t.Fatalf("superseded tool ran: %+v, %v", thread, err)
	}
}

func TestACPTicketPublishesActivityBeforeTheTurnFinishes(t *testing.T) {
	host, c, project, _ := testManagerHost(t)
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	value, err := host.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	connection := value.Agents.Connections["wire"]
	connection.Args[len(connection.Args)-1] = "activity-acp-helper"
	value.Agents.Connections["wire"] = connection
	if err := host.store.Save(value); err != nil {
		t.Fatal(err)
	}
	root := project.WorkspaceRoots[0]
	profile := subagent.Profile{Version: 1, Name: "activity-reader", Kind: subagent.AgentKindExternal, Agent: "wire", SystemPrompt: "Return findings."}
	if err := (model{hostState: hostState{store: host.store}}).customStoreAt(root).Save(profile, "workspace", nil); err != nil {
		t.Fatal(err)
	}
	item, candidate, a := claimInteractionTicket(t, host, c, project, "workspace/activity-reader")
	finished := make(chan error, 1)
	go func() {
		result, err := host.runTicket(ctx, c, project, item, candidate, a, "Inspect notes")
		if err == nil && result.Outcome != "succeeded" {
			err = fmt.Errorf("unexpected result %+v", result)
		}
		finished <- err
	}()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var active library.WorkerPage
	for active.ACPActivity == nil {
		select {
		case err := <-finished:
			t.Fatalf("ACP ended before the activity was visible: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
			active, err = c.ReadLatestWorker(ctx, a.ID, 20)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	activity := active.ACPActivity
	if active.WorkerKind != "acp" || active.AcceptsGuidance || activity.Response != "Found evidence" ||
		len(activity.Tools) != 1 || activity.Tools[0].Title != "Inspect notes" || activity.Tools[0].Status != "running" ||
		len(activity.Plan) != 1 || activity.Plan[0].Title != "Check evidence" || len(active.Messages) != 0 {
		t.Fatalf("unexpected live ACP page: %+v, activity=%+v", active, activity)
	}
	_, err = c.QueueTicketGuidance(ctx, a.ID, workitems.TicketGuidanceInput{Content: "Skip the review.", RequestID: "invalid-acp-guidance"})
	var response *library.ResponseError
	if !errors.As(err, &response) || response.StatusCode != http.StatusConflict {
		t.Fatalf("ACP accepted guidance: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "allow-acp-result"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	page, err := c.ReadLatestWorker(ctx, a.ID, 20)
	if err != nil || page.ACPActivity != nil || page.RunID != a.ID || len(page.Messages) != 2 || page.Messages[1].Content != "Found evidence in notes." {
		t.Fatalf("final ACP page: %+v, %v", page, err)
	}
}
