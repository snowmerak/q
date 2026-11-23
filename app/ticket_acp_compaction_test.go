package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workitems"
)

func TestACPTicketLateDecisionRetriesOnlyCompaction(t *testing.T) {
	host, c, project, _ := testManagerHost(t)
	ctx := t.Context()
	root := project.WorkspaceRoots[0]
	settings, err := host.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	connection := settings.Agents.Connections["wire"]
	connection.Args[len(connection.Args)-1] = "compaction-acp-helper"
	settings.Agents.Connections["wire"] = connection
	if err := host.store.Save(settings); err != nil {
		t.Fatal(err)
	}
	profile := subagent.Profile{Version: 1, Name: "compacting-reader", Kind: subagent.AgentKindExternal, Agent: "wire", SystemPrompt: "Return findings."}
	if err := (model{hostState: hostState{store: host.store}}).customStoreAt(root).Save(profile, "workspace", nil); err != nil {
		t.Fatal(err)
	}
	member, err := c.PutMember(ctx, project.ID, workitems.MemberInput{AgentID: "workspace/compacting-reader", DefinitionRoot: root, ExecutionRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	item, err := c.CreateItem(ctx, workitems.CreateInput{WorkspaceID: project.ID, Kind: workitems.Ticket, Title: "ACP compatibility", Content: "Check compatibility before changing behavior.", Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := host.coordinatorCandidates(ctx, project, item, []workitems.Member{member})
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%v err=%v", candidates, err)
	}
	candidate := candidates[0]
	claim := func(item workitems.Item, key string) workitems.Attempt {
		t.Helper()
		a, err := c.ClaimTicket(ctx, item.ID, workitems.ClaimInput{
			Revision: item.Revision, MemberID: member.ID, MemberRevision: member.Revision,
			Owner: "test", DefinitionDigest: candidate.Digest, RequestID: key,
		})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	transition := func(a workitems.Attempt, result subagent.TaskResult) workitems.Attempt {
		t.Helper()
		a, err := c.UpdateAttempt(ctx, a.ID, workitems.AttemptUpdate{
			Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: result.Outcome,
			UserQuestion: result.UserQuestion, PendingCompaction: result.PendingCompaction, OperationError: result.Blocker,
		})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	checkpoint := func(a workitems.Attempt) ticketACPCheckpoint {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(ticketWorkerStore(host.store.Dir, a).SessionDir(), "ticket-acp.json"))
		if err != nil {
			t.Fatal(err)
		}
		var value ticketACPCheckpoint
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	first := claim(item, "work")
	result, err := host.runTicket(ctx, c, project, item, candidate, first, "Inspect compatibility")
	if err != nil || result.Outcome != "blocked" || !result.PendingCompaction || result.UserQuestion != nil || !strings.Contains(result.Blocker, "no confirmed compaction") {
		t.Fatalf("unconfirmed compaction released the question: %+v, %v", result, err)
	}
	first = transition(first, result)
	prior := checkpoint(first)
	if !prior.Completed || prior.CompactionStatus != "" {
		t.Fatalf("work and compaction checkpoints were conflated: %+v", prior)
	}
	item, err = c.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	retry := func(key string) workitems.Attempt {
		t.Helper()
		current, err := c.GetItem(ctx, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		item, err = c.ControlTicket(ctx, current.ID, workitems.TicketAction{Action: "retry", Revision: current.Revision, RequestID: key})
		if err != nil {
			t.Fatal(err)
		}
		return claim(item, key+"-claim")
	}
	interrupted := retry("retry-before-startup")
	transition(interrupted, subagent.TaskResult{Outcome: "blocked", Blocker: "Agent was unavailable"})
	second := retry("retry-compaction")
	if second.CompactionAttemptID != first.ID || second.WorkerSessionID != first.WorkerSessionID {
		t.Fatalf("retry lost the checkpoint: %+v", second)
	}
	result, err = host.runTicket(ctx, c, project, item, candidate, second, "Do not repeat the original work")
	if err != nil || result.Outcome != workitems.WaitingUser || result.UserQuestion == nil || !result.UserQuestion.Late {
		t.Fatalf("confirmed compaction did not release the question: %+v, %v", result, err)
	}
	saved := checkpoint(second)
	if saved.SessionID != prior.SessionID || saved.Answer != prior.Answer || saved.CompactionStatus != "completed" {
		t.Fatalf("retry did not preserve the original ACP session and decision: %+v", saved)
	}
	session, err := ticketWorkerStore(host.store.Dir, second).Load()
	if err != nil || len(session.Transcript) != 2 {
		t.Fatalf("compaction repeated or replaced the work turn: %v, %v", session.Transcript, err)
	}
	second = transition(second, result)
	item, err = c.AnswerTicketQuestion(ctx, second.ID, workitems.AnswerInput{
		QuestionID: second.UserQuestion.ID, Answer: "Keep compatibility", RequestID: "answer",
	})
	if err != nil {
		t.Fatal(err)
	}
	third := claim(item, "resume")
	result, err = host.runTicket(ctx, c, project, item, candidate, third, "Use the persisted user answer")
	if err != nil || result.Outcome != "succeeded" || result.Report != "Applied Keep compatibility in "+prior.SessionID {
		t.Fatalf("decision did not reach the same ACP session: %+v, %v", result, err)
	}
}
