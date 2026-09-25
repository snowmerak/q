package app

import (
	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
)

const (
	skillHintsTag = "<q_skill_hints>"
	activeTaskTag = "<q_active_task>"
)

type SkillHint = agentloop.SkillHint
type skillHint = SkillHint
type SkillHintSet = agentloop.SkillHintSet
type skillHintSet = SkillHintSet

func knownSkillIDs(messages []client.Message) map[string]struct{} {
	return agentloop.KnownSkillIDs(messages)
}
func appendSkillHintContext(message client.Message, hints *skillHintSet) client.Message {
	return agentloop.AppendSkillHintContext(message, hints)
}
