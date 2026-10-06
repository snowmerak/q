package council

import (
	"fmt"
	"strings"
	"time"
)

// ExportMarkdown renders a complete, human-readable snapshot of one council.
// Turns may be incomplete when an export is requested during a run.
func ExportMarkdown(value Council, turns []Turn) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# %s\n\n", oneLine(value.Name))
	fmt.Fprintf(&out, "- Council ID: `%s`\n- Scope: %s\n- Configured rounds: %d\n", value.ID, value.Scope, EffectiveRounds(value))
	if value.WorkspaceRoot != "" {
		fmt.Fprintf(&out, "- Workspace: `%s`\n", value.WorkspaceRoot)
	}
	if value.ProjectID != "" {
		fmt.Fprintf(&out, "- Project ID: `%s`\n", value.ProjectID)
	}
	fmt.Fprintf(&out, "- Chair: `%s`\n\n", value.Chair.Model)
	if len(turns) == 0 {
		out.WriteString("_No council turns yet._\n")
		return out.String()
	}
	for index, turn := range turns {
		fmt.Fprintf(&out, "## Turn %d — %s\n\n", index+1, turn.CreatedAt.Format(time.RFC3339))
		fmt.Fprintf(&out, "**Status:** %s\n\n### Question\n\n%s\n\n", turn.Status, strings.TrimSpace(turn.Prompt))
		for _, round := range exportRounds(turn) {
			fmt.Fprintf(&out, "### Round %d — %s\n\n", round.Number, exportRoundName(round.Number))
			for responseIndex, response := range round.Responses {
				label, model := response.Label, response.Model
				if label == "" {
					label = string(rune('A' + responseIndex))
				}
				if model == "" && responseIndex < len(turn.Members) {
					model = turn.Members[responseIndex].Model
				}
				fmt.Fprintf(&out, "#### Answer %s — `%s`\n\n", label, model)
				writeExportText(&out, response.Text, response.Error)
			}
			for reviewIndex, review := range round.Reviews {
				model := review.Model
				if model == "" && reviewIndex < len(turn.Members) {
					model = turn.Members[reviewIndex].Model
				}
				fmt.Fprintf(&out, "#### Review — `%s`\n\n", model)
				writeExportText(&out, ReviewAnalysis(review.Text), review.Error)
			}
		}
		if turn.Final != "" {
			fmt.Fprintf(&out, "### Chair synthesis — `%s`\n\n%s\n\n", turn.Chair.Model, strings.TrimSpace(turn.Final))
		}
		if turn.Error != "" {
			fmt.Fprintf(&out, "**Turn error:** %s\n\n", turn.Error)
		}
	}
	return out.String()
}

func exportRounds(turn Turn) []Round {
	if len(turn.Rounds) > 0 {
		var result []Round
		for _, round := range turn.Rounds {
			if round.Number > 0 {
				result = append(result, round)
			}
		}
		return result
	}
	result := []Round{{Number: 1, Responses: turn.Responses}}
	if len(turn.Reviews) > 0 || len(turn.Ranking) > 0 || turn.Stage == "reviews" || turn.Stage == "synthesis" || turn.Status == "completed" {
		result = append(result, Round{Number: 2, Reviews: turn.Reviews, Ranking: turn.Ranking})
	}
	return result
}

func exportRoundName(number int) string {
	switch number {
	case 1:
		return "Independent answers"
	case 2:
		return "Peer review"
	default:
		return "Integrated review"
	}
}

func writeExportText(out *strings.Builder, content, issue string) {
	if issue != "" {
		fmt.Fprintf(out, "**Error:** %s\n\n", issue)
	} else if strings.TrimSpace(content) != "" {
		fmt.Fprintf(out, "%s\n\n", strings.TrimSpace(content))
	} else {
		out.WriteString("_Pending._\n\n")
	}
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
