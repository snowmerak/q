package app

import (
	"strings"
	"time"

	"github.com/snowmerak/q/client"
)

func (m *model) touchSessionMetadata(source string) bool {
	titleChanged := false
	if m.sessionTitle == "" {
		m.sessionTitle = compactSessionTitle(source)
		titleChanged = m.sessionTitle != ""
	}
	m.sessionUpdatedAt = time.Now().UTC()
	return titleChanged
}

func compactSessionTitle(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	const maximumRunes = 80
	runes := []rune(value)
	if len(runes) <= maximumRunes {
		return value
	}
	return strings.TrimSpace(string(runes[:maximumRunes-1])) + "…"
}

func sessionTitleFromMessages(messages []client.Message) string {
	for _, message := range messages {
		if message.Role == client.RoleUser {
			if title := compactSessionTitle(message.TextContent()); title != "" {
				return title
			}
		}
	}
	return ""
}
