package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/snowmerak/q/client"
	"gopkg.in/yaml.v3"
)

const (
	checkpointHeading    = "Session continuation checkpoint:\n"
	legacySummaryHeading = "Compressed conversation memory:\n"
)

// Checkpoint is the bounded state needed to continue one session after older
// conversation messages have been removed from the provider request.
type Checkpoint struct {
	CurrentRequest []string `json:"current_request"`
	ActiveWork     []string `json:"active_work"`
	PreviousWork   []string `json:"previous_work"`
	Facts          []string `json:"facts"`
}

type checkpointUpdate struct {
	checkpoint Checkpoint
	present    [4]bool
}

func normalizeCheckpoint(plan Plan, response string) (string, error) {
	update, err := parseCheckpointUpdate(response)
	if err != nil {
		return "", err
	}
	checkpoint, _ := priorCheckpoint(plan)
	if update.present[0] {
		checkpoint.CurrentRequest = update.checkpoint.CurrentRequest
	}
	if update.present[1] {
		checkpoint.ActiveWork = update.checkpoint.ActiveWork
	}
	if update.present[2] {
		checkpoint.PreviousWork = update.checkpoint.PreviousWork
	}
	if update.present[3] {
		checkpoint.Facts = update.checkpoint.Facts
	}
	checkpoint = compactCheckpoint(checkpoint)
	if !checkpointHasContent(checkpoint) {
		return "", errors.New("memory: provider returned an empty session checkpoint")
	}
	body, err := json.Marshal(checkpoint)
	if err != nil {
		return "", fmt.Errorf("memory: encode session checkpoint: %w", err)
	}
	return string(body), nil
}

func priorCheckpoint(plan Plan) (Checkpoint, bool) {
	for _, messages := range [][]client.Message{plan.Immutable, plan.Source, plan.RetainedSkillResources, plan.Recent} {
		for _, message := range messages {
			if message.Name != SummaryName {
				continue
			}
			update, err := parseCheckpointUpdate(message.Content)
			if err == nil {
				return compactCheckpoint(update.checkpoint), true
			}
		}
	}
	return Checkpoint{}, false
}

func parseCheckpointUpdate(response string) (checkpointUpdate, error) {
	object, err := decodeRecoverableJSONObject(response)
	if err != nil {
		return checkpointUpdate{}, fmt.Errorf("memory: decode session checkpoint: %w", err)
	}
	object = unwrapCheckpointObject(object)
	var update checkpointUpdate
	for key, value := range object {
		items := checkpointItems(value, "")
		switch checkpointSection(key) {
		case 0:
			update.present[0] = true
			update.checkpoint.CurrentRequest = append(update.checkpoint.CurrentRequest, items...)
		case 1:
			update.present[1] = true
			update.checkpoint.ActiveWork = append(update.checkpoint.ActiveWork, items...)
		case 2:
			update.present[2] = true
			update.checkpoint.PreviousWork = append(update.checkpoint.PreviousWork, items...)
		case 3:
			update.present[3] = true
			update.checkpoint.Facts = append(update.checkpoint.Facts, items...)
		}
	}
	if !update.present[0] && !update.present[1] && !update.present[2] && !update.present[3] {
		return checkpointUpdate{}, errors.New("no recognized checkpoint sections")
	}
	update.checkpoint = compactCheckpoint(update.checkpoint)
	return update, nil
}

func unwrapCheckpointObject(object map[string]any) map[string]any {
	for key := range object {
		if checkpointSection(key) >= 0 {
			return object
		}
	}
	for key, value := range object {
		switch normalizeCheckpointKey(key) {
		case "checkpoint", "sessioncheckpoint", "continuationcheckpoint", "result":
			if nested, ok := value.(map[string]any); ok {
				return nested
			}
		}
	}
	return object
}

func checkpointSection(key string) int {
	switch normalizeCheckpointKey(key) {
	case "currentrequest", "request", "userrequest", "currentgoal", "objective", "현재요청", "요청":
		return 0
	case "activework", "currentwork", "inprogress", "workingon", "active", "현재작업", "진행중":
		return 1
	case "previouswork", "priorwork", "history", "completedwork", "recentwork", "이전작업", "작업이력", "완료작업":
		return 2
	case "facts", "durablefacts", "importantfacts", "persistentfacts", "knownfacts", "사실", "핵심사실", "유지사실":
		return 3
	default:
		return -1
	}
}

func normalizeCheckpointKey(key string) string {
	var normalized strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(key)) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			normalized.WriteRune(r)
		}
	}
	return normalized.String()
}

func checkpointItems(value any, prefix string) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		value := strings.TrimSpace(typed)
		if value == "" {
			return nil
		}
		if prefix != "" {
			value = prefix + ": " + value
		}
		return []string{value}
	case []any:
		var result []string
		for _, item := range typed {
			result = append(result, checkpointItems(item, prefix)...)
		}
		return result
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var result []string
		for _, key := range keys {
			label := strings.TrimSpace(key)
			if prefix != "" {
				label = prefix + "." + label
			}
			result = append(result, checkpointItems(typed[key], label)...)
		}
		return result
	default:
		body, err := json.Marshal(typed)
		if err != nil {
			return nil
		}
		value := string(body)
		if prefix != "" {
			value = prefix + ": " + value
		}
		return []string{value}
	}
}

func compactCheckpoint(checkpoint Checkpoint) Checkpoint {
	checkpoint.CurrentRequest = compactCheckpointItems(checkpoint.CurrentRequest)
	checkpoint.ActiveWork = compactCheckpointItems(checkpoint.ActiveWork)
	checkpoint.PreviousWork = compactCheckpointItems(checkpoint.PreviousWork)
	checkpoint.Facts = compactCheckpointItems(checkpoint.Facts)
	return checkpoint
}

func compactCheckpointItems(items []string) []string {
	result := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, found := seen[item]; found {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

func checkpointHasContent(checkpoint Checkpoint) bool {
	return len(checkpoint.CurrentRequest)+len(checkpoint.ActiveWork)+len(checkpoint.PreviousWork)+len(checkpoint.Facts) > 0
}

func decodeRecoverableJSONObject(response string) (map[string]any, error) {
	var lastErr error
	for _, candidate := range checkpointJSONCandidates(response) {
		variants := []string{candidate, escapeJSONControlCharacters(candidate)}
		variants = append(variants, removeTrailingJSONCommas(variants[0]), removeTrailingJSONCommas(variants[1]))
		seen := make(map[string]struct{}, len(variants))
		for _, variant := range variants {
			if _, found := seen[variant]; found {
				continue
			}
			seen[variant] = struct{}{}
			var object map[string]any
			if err := json.Unmarshal([]byte(variant), &object); err == nil && object != nil {
				if checkpointObjectHasRecognizedSection(object) {
					return object, nil
				}
				lastErr = errors.New("no recognized checkpoint sections")
			} else if err != nil {
				lastErr = err
			}
		}
		for _, variant := range variants {
			if _, found := seen["yaml:"+variant]; found {
				continue
			}
			seen["yaml:"+variant] = struct{}{}
			var object map[string]any
			if err := yaml.Unmarshal([]byte(variant), &object); err == nil && object != nil {
				if checkpointObjectHasRecognizedSection(object) {
					return object, nil
				}
				lastErr = errors.New("no recognized checkpoint sections")
			} else if err != nil {
				lastErr = err
			}
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no complete JSON object found")
	}
	return nil, lastErr
}

func checkpointObjectHasRecognizedSection(object map[string]any) bool {
	for key := range unwrapCheckpointObject(object) {
		if checkpointSection(key) >= 0 {
			return true
		}
	}
	return false
}

func checkpointJSONCandidates(response string) []string {
	value := strings.TrimSpace(strings.TrimPrefix(response, "\ufeff"))
	value = strings.TrimSpace(strings.TrimPrefix(value, checkpointHeading))
	value = strings.TrimSpace(strings.TrimPrefix(value, legacySummaryHeading))
	candidates := make([]string, 0, 4)
	add := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		for _, existing := range candidates {
			if existing == candidate {
				return
			}
		}
		candidates = append(candidates, candidate)
	}
	add(value)
	if strings.HasPrefix(value, "```") {
		if newline, closing := strings.IndexByte(value, '\n'), strings.LastIndex(value, "```"); newline >= 0 && closing > newline {
			add(value[newline+1 : closing])
		}
	}
	for start := 0; start < len(value); {
		relative := strings.IndexByte(value[start:], '{')
		if relative < 0 {
			break
		}
		start += relative
		if end := balancedJSONObjectEnd(value, start); end > start {
			add(value[start:end])
			start = end
		} else {
			start++
		}
	}
	return candidates
}

func balancedJSONObjectEnd(value string, start int) int {
	depth := 0
	var quote byte
	escaped := false
	for index := start; index < len(value); index++ {
		current := value[index]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if current == '\\' {
				escaped = true
				continue
			}
			if current == quote {
				quote = 0
			}
			continue
		}
		switch current {
		case '\'', '"':
			quote = current
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return index + 1
			}
			if depth < 0 {
				return -1
			}
		}
	}
	return -1
}

func escapeJSONControlCharacters(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	inString := false
	escaped := false
	for index := 0; index < len(value); index++ {
		current := value[index]
		if !inString {
			result.WriteByte(current)
			if current == '"' {
				inString = true
			}
			continue
		}
		if escaped {
			result.WriteByte(current)
			escaped = false
			continue
		}
		if current == '\\' {
			result.WriteByte(current)
			escaped = true
			continue
		}
		if current == '"' {
			result.WriteByte(current)
			inString = false
			continue
		}
		switch current {
		case '\n':
			result.WriteString(`\n`)
		case '\r':
			result.WriteString(`\r`)
		case '\t':
			result.WriteString(`\t`)
		default:
			if current < 0x20 {
				fmt.Fprintf(&result, `\u%04x`, current)
			} else {
				result.WriteByte(current)
			}
		}
	}
	return result.String()
}

func removeTrailingJSONCommas(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	var quote byte
	escaped := false
	for index := 0; index < len(value); index++ {
		current := value[index]
		if quote != 0 {
			result.WriteByte(current)
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == quote {
				quote = 0
			}
			continue
		}
		if current == '\'' || current == '"' {
			quote = current
			result.WriteByte(current)
			continue
		}
		if current == ',' {
			next := index + 1
			for next < len(value) && unicode.IsSpace(rune(value[next])) {
				next++
			}
			if next < len(value) && (value[next] == '}' || value[next] == ']') {
				continue
			}
		}
		result.WriteByte(current)
	}
	return result.String()
}
