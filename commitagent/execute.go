package commitagent

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

type Result struct {
	Messages     []string
	AutoStaged   bool
	UsedFallback bool
	Split        bool
}

func executeProposal(ctx context.Context, state repositoryState, proposal proposalState, source string, logger *progressLogger) (Result, error) {
	fallback := source == "mechanical fallback"
	if proposal.Single != nil {
		message := formatCommitMessage(*proposal.Single)
		logger.step("commit", "creating %s", formatSubject(*proposal.Single))
		_, err := commitIndex(ctx, state.root, *proposal.Single)
		if err != nil {
			return Result{}, err
		}
		logger.step("commit", "created %s", formatSubject(*proposal.Single))
		return Result{Messages: []string{message}, AutoStaged: state.autoStaged, UsedFallback: fallback}, nil
	}
	if len(proposal.Split) == 0 {
		return Result{}, fmt.Errorf("q commit: proposal is empty")
	}
	logger.step("split", "preparing %d index patches", len(proposal.Split))
	messages, err := executeSplit(ctx, state, proposal.Split, logger)
	if err != nil {
		return Result{}, err
	}
	return Result{Messages: messages, AutoStaged: state.autoStaged, Split: true}, nil
}

func commitIndex(ctx context.Context, root string, proposal Proposal) (string, error) {
	arguments := []string{"commit", "-m", formatSubject(proposal)}
	if len(proposal.Body) > 0 {
		lines := make([]string, 0, len(proposal.Body))
		for _, line := range proposal.Body {
			lines = append(lines, "- "+line)
		}
		arguments = append(arguments, "-m", strings.Join(lines, "\n"))
	}
	body, err := gitOutput(ctx, root, arguments...)
	if err != nil {
		return "", fmt.Errorf("q commit: create commit: %w", err)
	}
	return body, nil
}

func executeSplit(ctx context.Context, state repositoryState, proposals []Proposal, logger *progressLogger) ([]string, error) {
	patches := make([]string, len(proposals))
	for index, proposal := range proposals {
		var patch strings.Builder
		for _, path := range proposal.Files {
			patch.WriteString(state.fileDiffs[path])
		}
		if index == 0 {
			for _, path := range state.lockFiles {
				patch.WriteString(state.fileDiffs[path])
			}
		}
		patches[index] = patch.String()
	}
	if _, err := gitOutput(ctx, state.root, "reset", "--quiet"); err != nil {
		return nil, fmt.Errorf("q commit: prepare split index: %w", err)
	}

	messages := make([]string, 0, len(proposals))
	for index, proposal := range proposals {
		logger.step("split", "creating commit %d/%d: %s", index+1, len(proposals), formatSubject(proposal))
		if err := applyIndexPatch(ctx, state.root, patches[index]); err != nil {
			restoreErr := restoreRemainingPatches(ctx, state.root, patches[index:])
			return nil, errorsJoin(fmt.Errorf("q commit: stage split %d: %w", index+1, err), restoreErr)
		}
		_, err := commitIndex(ctx, state.root, proposal)
		if err != nil {
			restoreErr := restoreRemainingPatches(ctx, state.root, patches[index+1:])
			return nil, errorsJoin(err, restoreErr)
		}
		message := formatCommitMessage(proposal)
		messages = append(messages, message)
		logger.step("split", "created commit %d/%d", index+1, len(proposals))
	}
	return messages, nil
}

func restoreRemainingPatches(ctx context.Context, root string, patches []string) error {
	for _, patch := range patches {
		if strings.TrimSpace(patch) == "" {
			continue
		}
		if err := applyIndexPatch(ctx, root, patch); err != nil {
			return fmt.Errorf("q commit: restore remaining staged changes: %w", err)
		}
	}
	return nil
}

func applyIndexPatch(ctx context.Context, root, patch string) error {
	if strings.TrimSpace(patch) == "" {
		return fmt.Errorf("split patch is empty")
	}
	_, err := gitBytes(ctx, root, bytes.NewBufferString(patch), "apply", "--cached", "--binary", "--whitespace=nowarn", "-")
	return err
}

func errorsJoin(primary, secondary error) error {
	if secondary == nil {
		return primary
	}
	return fmt.Errorf("%w; additionally, %v", primary, secondary)
}
