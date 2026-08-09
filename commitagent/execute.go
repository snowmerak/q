package commitagent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
)

type Result struct {
	Messages     []string
	AutoStaged   bool
	UsedFallback bool
	Split        bool
}

func executeProposal(ctx context.Context, state repositoryState, proposal proposalState, source string, output io.Writer) (Result, error) {
	fallback := source == "mechanical fallback"
	if proposal.Single != nil {
		message := formatCommitMessage(*proposal.Single)
		commandOutput, err := commitIndex(ctx, state.root, *proposal.Single)
		if err != nil {
			return Result{}, err
		}
		writeCommitOutput(output, commandOutput, message, source)
		return Result{Messages: []string{message}, AutoStaged: state.autoStaged, UsedFallback: fallback}, nil
	}
	if len(proposal.Split) == 0 {
		return Result{}, fmt.Errorf("q commit: proposal is empty")
	}
	messages, err := executeSplit(ctx, state, proposal.Split, output)
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

func executeSplit(ctx context.Context, state repositoryState, proposals []Proposal, output io.Writer) ([]string, error) {
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
		if err := applyIndexPatch(ctx, state.root, patches[index]); err != nil {
			restoreErr := restoreRemainingPatches(ctx, state.root, patches[index:])
			return nil, errorsJoin(fmt.Errorf("q commit: stage split %d: %w", index+1, err), restoreErr)
		}
		commandOutput, err := commitIndex(ctx, state.root, proposal)
		if err != nil {
			restoreErr := restoreRemainingPatches(ctx, state.root, patches[index+1:])
			return nil, errorsJoin(err, restoreErr)
		}
		message := formatCommitMessage(proposal)
		messages = append(messages, message)
		writeCommitOutput(output, commandOutput, message, "commit agent")
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

func writeCommitOutput(output io.Writer, gitOutput, message, source string) {
	if output == nil {
		return
	}
	if strings.TrimSpace(gitOutput) != "" {
		fmt.Fprintln(output, strings.TrimSpace(gitOutput))
	}
	if source == "" {
		source = "commit agent"
	}
	fmt.Fprintf(output, "q commit · %s\n%s\n", source, message)
}

func errorsJoin(primary, secondary error) error {
	if secondary == nil {
		return primary
	}
	return fmt.Errorf("%w; additionally, %v", primary, secondary)
}
