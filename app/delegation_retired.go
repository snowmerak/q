package app

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/workspace"
)

const directDelegationRetired = "direct subagent execution was removed; use workspace_members and ticket_create in a Studio project for asynchronous System One assignment"

func retiredDelegationResult() client.ToolResult {
	return client.ToolResult{Content: directDelegationRetired, IsError: true}
}

// Recover only an already saved result. Reopening a parent must never create,
// resume, or replay a legacy child outside the ticket assignment boundary.
func recoverRetiredDelegate(ctx context.Context, parent workspace.Store, call client.ToolCall) (client.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return client.ToolResult{}, err
	}
	if call.ID == "" {
		return retiredDelegationResult(), nil
	}
	session, err := parent.Load()
	if errors.Is(err, workspace.ErrNotFound) {
		return retiredDelegationResult(), nil
	}
	if err != nil {
		return client.ToolResult{}, err
	}
	callIndex, toolIndex, err := pendingDelegationPosition(session.Transcript, call)
	if err != nil {
		return retiredDelegationResult(), nil
	}
	bookmarks, err := parent.LoadDelegations()
	if err != nil {
		return client.ToolResult{}, err
	}
	for _, bookmark := range bookmarks {
		if bookmark.CallIndex != callIndex || bookmark.ToolIndex != toolIndex {
			continue
		}
		if bookmark.CallID != call.ID || bookmark.RunID != session.RunID {
			return client.ToolResult{}, errors.New("saved delegation bookmark does not match the parent call")
		}
		var input delegateInput
		if err := decodeDelegationArguments(call.Function.Arguments, &input); err != nil {
			return client.ToolResult{}, err
		}
		if strings.TrimSpace(input.SubagentName) != bookmark.Agent || strings.TrimSpace(input.Prompt) != bookmark.Prompt {
			return client.ToolResult{}, errors.New("saved delegation input does not match bookmark")
		}
		child, err := parent.ChildStore(bookmark.InvocationID)
		if err != nil {
			return client.ToolResult{}, err
		}
		state, err := child.LoadDelegationState()
		if errors.Is(err, os.ErrNotExist) {
			return retiredDelegationResult(), nil
		}
		if err != nil {
			return client.ToolResult{}, err
		}
		if state.Agent != bookmark.Agent || state.Prompt != bookmark.Prompt || state.RunID != bookmark.RunID || state.WorkingDirectory != bookmark.WorkingDirectory {
			return client.ToolResult{}, errors.New("saved delegation state does not match bookmark")
		}
		if state.Result != nil {
			return *state.Result, nil
		}
		return retiredDelegationResult(), nil
	}
	return retiredDelegationResult(), nil
}
