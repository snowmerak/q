package tools

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/snowmerak/q/client/systemone"
	"github.com/snowmerak/q/systemoneconfig"
	"github.com/snowmerak/q/tools/builtin"
)

const (
	maximumSkillDecisionQueryBytes       = 4000
	maximumSkillDecisionTitleBytes       = 160
	maximumSkillDecisionDescriptionBytes = 600
	maximumSkillDecisionTags             = 8
	maximumSkillDecisionTagBytes         = 64
)

var skillRelevanceCriteria = []string{
	"Unrelated to the task or likely to mislead the work.",
	"Only weakly related and unlikely to provide useful instructions.",
	"Useful for a meaningful part of the task.",
	"A strong match that should guide how the task is performed.",
	"A direct and essential match for the task.",
}

type systemOneSkillRanker struct {
	store systemoneconfig.Store
}

// WithSystemOneSkillRanking enables live System One relevance decisions when
// the supplied store contains System One settings.
func WithSystemOneSkillRanking(store systemoneconfig.Store) RuntimeOption {
	return func(options *runtimeOptions) {
		options.skillRanker = &systemOneSkillRanker{store: store}
	}
}

func (r *systemOneSkillRanker) Enabled() bool {
	if r == nil {
		return false
	}
	_, err := os.Stat(r.store.Path())
	return err == nil
}

func (r *systemOneSkillRanker) RankSkills(
	ctx context.Context,
	query string,
	hits []builtin.SkillSearchHit,
) ([]builtin.SkillSearchHit, error) {
	if len(hits) == 0 {
		return nil, nil
	}
	configured, model, err := r.store.NewClientForRole(systemoneconfig.RoleAgentSkillDecision)
	if err != nil {
		return nil, err
	}
	questions := make(map[string]systemone.Question, len(hits))
	for index, hit := range hits {
		questions[skillDecisionQuestionName(index)] = systemone.Question{
			Type: systemone.QuestionScore,
			Instructions: map[string]any{
				"task": "Evaluate whether this Agent Skill should guide the task in the shared state.",
				"candidate": map[string]any{
					"name":        truncateSkillDecisionText(hit.Title, maximumSkillDecisionTitleBytes),
					"description": truncateSkillDecisionText(hit.Description, maximumSkillDecisionDescriptionBytes),
					"tags":        boundedSkillDecisionTags(hit.Tags),
					"scope":       hit.Scope,
				},
			},
			Criteria: skillRelevanceCriteria,
		}
	}
	result, err := configured.Evaluate(ctx, systemone.Request{
		Model: model,
		State: map[string]string{
			"task": truncateSkillDecisionText(strings.TrimSpace(query), maximumSkillDecisionQueryBytes),
		},
		Questions: questions,
	}, systemone.CallOptions{})
	if err != nil {
		return nil, err
	}
	ranked := append([]builtin.SkillSearchHit(nil), hits...)
	for index := range ranked {
		answer, found := result.Answers[skillDecisionQuestionName(index)]
		if !found || answer.Type != systemone.QuestionScore || answer.Score == nil {
			return nil, fmt.Errorf("systemone: missing score for skill candidate %d", index)
		}
		score := *answer.Score
		if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > float64(len(skillRelevanceCriteria)-1) {
			return nil, fmt.Errorf("systemone: invalid score for skill candidate %d", index)
		}
		ranked[index].Score = score
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].Score > ranked[j].Score
	})
	return ranked, nil
}

func skillDecisionQuestionName(index int) string {
	return fmt.Sprintf("candidate_%02d", index)
}

func boundedSkillDecisionTags(tags []string) []string {
	result := make([]string, 0, min(len(tags), maximumSkillDecisionTags))
	for _, tag := range tags {
		if len(result) == maximumSkillDecisionTags {
			break
		}
		value := truncateSkillDecisionText(strings.TrimSpace(tag), maximumSkillDecisionTagBytes)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func truncateSkillDecisionText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}

var _ builtin.SkillRanker = (*systemOneSkillRanker)(nil)
