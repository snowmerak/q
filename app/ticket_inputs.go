package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/snowmerak/q/internal/workmedia"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/workitems"
)

func ticketAnswerPrompt(answer *workitems.UserQuestion) string {
	reply, _ := json.Marshal(answer)
	return "Recorded user decision for this ticket:\n" + string(reply) + "\nApply this answer to the decision described. Verify the current project or worktree state before continuing within the approved ticket scope."
}

func ticketImages(ctx context.Context, c *library.Client, item workitems.Item, attempt workitems.Attempt) ([]ImageInput, error) {
	if attempt.PreviousAnswer != nil || attempt.CompactionAttemptID != "" {
		return nil, nil
	}
	body, err := c.ReadPostRevision(ctx, item.BodyPostID, attempt.BodyRevision)
	if err != nil {
		return nil, err
	}
	refs := append([]string(nil), body.Refs...)
	issueID := item.LinkedIssueID
	if attempt.ReviewChildAttemptID != "" {
		child, err := c.GetAttempt(ctx, attempt.ReviewChildAttemptID)
		if err != nil {
			return nil, err
		}
		ticket, err := c.GetItem(ctx, child.TicketID)
		if err != nil {
			return nil, err
		}
		body, err := c.ReadPostRevision(ctx, ticket.BodyPostID, child.BodyRevision)
		if err != nil {
			return nil, err
		}
		refs = body.Refs
		issueID = ticket.LinkedIssueID
	}
	if issueID != "" {
		issue, err := c.GetItem(ctx, issueID)
		if err != nil {
			return nil, err
		}
		post, err := c.ReadContent(ctx, issue.BodyPostID, "", 1)
		if err != nil {
			return nil, err
		}
		refs = append(refs, post.Post.Refs...)
	}
	images := make([]ImageInput, 0, workmedia.MaxImages)
	seen := map[string]bool{}
	bytes := 0
	for _, ref := range refs {
		id, ok := strings.CutPrefix(ref, workitems.AssetPrefix)
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		asset, err := c.GetWorkAsset(ctx, item.WorkspaceID, id)
		if err != nil {
			return nil, fmt.Errorf("load ticket attachment: %w", err)
		}
		if workmedia.Supported(asset.MIME) && bytes+len(asset.Data) <= workmedia.MaxTotalBytes {
			images = append(images, ImageInput{Data: asset.Data})
			bytes += len(asset.Data)
		}
		if len(images) == workmedia.MaxImages {
			break
		}
	}
	return images, nil
}
