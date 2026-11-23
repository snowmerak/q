package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/internal/workmedia"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/tools/builtin"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/workspace"
)

func acpTicketResult(answer string, decisions bool) subagent.TaskResult {
	result := subagent.TaskResult{Outcome: "succeeded", Summary: "ACP worker completed", Report: strings.TrimSpace(answer)}
	if decisions {
		if _, data, found := fencedTicketResult(answer, "q-user-decision"); found {
			var q workitems.UserQuestion
			if json.Unmarshal([]byte(data), &q) != nil || q.Validate() != nil || q.ID != "" || q.Answer != "" {
				return subagent.TaskResult{Outcome: "blocked", Summary: "Invalid ACP decision request", Blocker: "The ACP worker sent an invalid q-user-decision response."}
			}
			if q.Late {
				return subagent.TaskResult{Outcome: "blocked", Summary: "ACP decision requires a compacted checkpoint", Blocker: "Resolve this decision in a follow-up ticket. Q cannot confirm compaction of this ACP session.", Report: q.Question + "\n\n" + q.Context}
			}
			return subagent.TaskResult{Outcome: workitems.WaitingUser, Summary: q.Question, UserQuestion: &q}
		}
	}
	body, encoded, ok := fencedTicketResult(answer, "q-verification")
	if !ok {
		return result
	}
	var value struct {
		Verification []string `json:"verification"`
		Artifacts    []string `json:"artifacts"`
	}
	if json.Unmarshal([]byte(encoded), &value) == nil {
		result.Report = strings.TrimSpace(body)
		result.Verification, result.Artifacts = value.Verification, value.Artifacts
	}
	return result
}

func fencedTicketResult(answer, tag string) (string, string, bool) {
	body, block, ok := strings.Cut("\n"+strings.TrimSpace(answer), "\n```"+tag+"\n")
	if !ok {
		return "", "", false
	}
	encoded, suffix, ok := strings.Cut(block, "\n```")
	return body, encoded, ok && strings.TrimSpace(suffix) == "" && len(encoded) <= 16<<10
}

func saveTicketVerification(ctx context.Context, c *library.Client, a workitems.Attempt, worker workspace.Store, root string, result *subagent.TaskResult) error {
	var report strings.Builder
	fmt.Fprintf(&report, "Ticket %s\nAttempt %s\nTicket revision %d, body revision %d\n", a.TicketID, a.ID, a.TicketRevision, a.BodyRevision)
	if result.ChangeRequest != nil && result.ChangeRequest.HeadCommit != "" {
		fmt.Fprintf(&report, "Submitted commit %s\n", result.ChangeRequest.HeadCommit)
	}
	fmt.Fprintf(&report, "\nOutcome: %s\n%s\n", result.Outcome, result.Summary)
	if len(a.Acceptance) != 0 {
		fmt.Fprintln(&report, "\nAcceptance criteria")
		for _, criterion := range a.Acceptance {
			fmt.Fprintln(&report, "-", criterion)
		}
	}
	fmt.Fprintln(&report, "\nWorker-reported checks")
	if len(result.Verification) == 0 {
		fmt.Fprintln(&report, "No checks reported.")
	}
	for _, check := range result.Verification {
		fmt.Fprintln(&report, "-", check)
	}
	if session, err := worker.Load(); err == nil {
		start := 0
		for i, message := range session.Transcript {
			if message.Role == client.RoleUser && message.Name == subagent.FollowupMessageName {
				start = i
			}
		}
		completed := map[string]bool{}
		for _, message := range session.Transcript[start:] {
			if message.Role != client.RoleTool || (message.Name != "run_command" && message.Name != "cmd_status" && message.Name != "wait") {
				continue
			}
			var command builtin.CommandOutput
			if json.Unmarshal([]byte(message.Content), &command) != nil || command.CommandID == "" || command.ExitCode == nil || completed[command.CommandID] {
				continue
			}
			if len(completed) == 0 {
				fmt.Fprintln(&report, "\nObserved command results")
			}
			completed[command.CommandID] = true
			fmt.Fprintf(&report, "- %s (%s), exit code %d\n", command.CommandID, command.Status, *command.ExitCode)
		}
	}
	var files []subagent.VerificationFile
	seen := map[string]bool{}
	if len(result.Artifacts) != 0 {
		opened, err := os.OpenRoot(root)
		if err != nil {
			return err
		}
		defer opened.Close()
		for _, name := range result.Artifacts {
			if seen[name] || len(seen) >= 8 || strings.Contains(name, "://") {
				continue
			}
			seen[name] = true
			data, err := readVerificationFile(opened, name)
			if err != nil {
				fmt.Fprintf(&report, "\nCould not attach %s: %v\n", name, err)
				continue
			}
			asset, err := c.PutWorkAsset(ctx, a.WorkspaceID, library.AssetUpload{Data: data})
			if err != nil {
				fmt.Fprintf(&report, "\nCould not attach %s: %v\n", name, err)
				continue
			}
			files = append(files, subagent.VerificationFile{ID: asset.ID, Name: name, MIME: asset.MIME})
		}
	}
	asset, err := c.PutWorkAsset(ctx, a.WorkspaceID, library.AssetUpload{Data: []byte(report.String())})
	if err != nil {
		return err
	}
	result.VerificationFiles = append([]subagent.VerificationFile{{ID: asset.ID, Name: "Verification report.txt", MIME: asset.MIME}}, files...)
	return nil
}

func readVerificationFile(root *os.Root, name string) ([]byte, error) {
	name = filepath.Clean(filepath.FromSlash(name))
	if !filepath.IsLocal(name) || strings.EqualFold(name, ".git") || strings.HasPrefix(strings.ToLower(name), ".git"+string(filepath.Separator)) {
		return nil, fmt.Errorf("expected a project-relative file")
	}
	info, err := root.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > workmedia.MaxImageBytes {
		return nil, fmt.Errorf("expected a regular file of at most 4 MiB")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, workmedia.MaxImageBytes+1))
}
