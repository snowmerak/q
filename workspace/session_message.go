package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/thinker"
)

// sessionFile is Q's durable conversation format. Transport adapters convert
// its messages to Chat Completions or Responses only when making a request.
type sessionFile struct {
	Version          int                   `json:"version"`
	ID               string                `json:"id,omitempty"`
	RunID            string                `json:"run_id,omitempty"`
	LoopMode         string                `json:"loop_mode,omitempty"`
	Title            string                `json:"title,omitempty"`
	UpdatedAt        *time.Time            `json:"updated_at,omitempty"`
	Transcript       []sessionMessage      `json:"transcript,omitempty"`
	Context          []sessionMessage      `json:"context,omitempty"`
	ResponseReplay   []ResponseReplayItem  `json:"response_replay,omitempty"`
	ResponseAffinity *ResponseAffinity     `json:"response_affinity,omitempty"`
	Learning         thinker.LearningState `json:"learning"`
	ActiveTask       *ActiveTask           `json:"active_task,omitempty"`
}

type sessionMessage struct {
	Role       client.Role       `json:"role"`
	Phase      string            `json:"phase,omitempty"`
	Content    []sessionPart     `json:"content,omitempty"`
	Name       string            `json:"name,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	ToolCalls  []sessionToolCall `json:"tool_calls,omitempty"`
}

type sessionPart struct {
	Kind   string                    `json:"kind"`
	Text   string                    `json:"text,omitempty"`
	URL    string                    `json:"url,omitempty"`
	FileID string                    `json:"file_id,omitempty"`
	Detail string                    `json:"detail,omitempty"`
	Extra  map[string]any            `json:"extra,omitempty"`
	Raw    client.MessageContentPart `json:"raw,omitempty"`
}

type sessionToolCall struct {
	Index     int             `json:"index,omitempty"`
	ID        string          `json:"id"`
	Type      client.ToolType `json:"type"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
}

func (s Session) MarshalJSON() ([]byte, error) {
	return json.Marshal(sessionFile{
		Version: CurrentVersion, ID: s.ID, RunID: s.RunID, LoopMode: s.LoopMode, Title: s.Title, UpdatedAt: s.UpdatedAt,
		Transcript: toSessionMessages(s.Transcript), Context: toSessionMessages(s.Context),
		ResponseReplay: s.ResponseReplay, ResponseAffinity: s.ResponseAffinity,
		Learning: s.Learning, ActiveTask: s.ActiveTask,
	})
}

func (s *Session) UnmarshalJSON(data []byte) error {
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return err
	}
	switch header.Version {
	case 1:
		// Version 1 stored Chat wire messages. Decode them once and write the
		// portable format on the next ordinary session save.
		type legacySession Session
		var legacy legacySession
		if err := decodeSessionStrict(data, &legacy); err != nil {
			return err
		}
		*s = Session(legacy)
		s.Version = CurrentVersion
		return nil
	case CurrentVersion:
		var saved sessionFile
		if err := decodeSessionStrict(data, &saved); err != nil {
			return err
		}
		if err := validateSessionMessages(saved.Transcript); err != nil {
			return err
		}
		if err := validateSessionMessages(saved.Context); err != nil {
			return err
		}
		*s = Session{
			Version: CurrentVersion, ID: saved.ID, RunID: saved.RunID, LoopMode: saved.LoopMode, Title: saved.Title, UpdatedAt: saved.UpdatedAt,
			Transcript: fromSessionMessages(saved.Transcript), Context: fromSessionMessages(saved.Context),
			ResponseReplay: saved.ResponseReplay, ResponseAffinity: saved.ResponseAffinity,
			Learning: saved.Learning, ActiveTask: saved.ActiveTask,
		}
		return nil
	default:
		return fmt.Errorf("workspace: unsupported session version %d", header.Version)
	}
}

func validateSessionMessages(messages []sessionMessage) error {
	for _, message := range messages {
		for _, part := range message.Content {
			switch part.Kind {
			case "text":
			case "image":
				if (part.URL == "") == (part.FileID == "") {
					return fmt.Errorf("workspace: session image must have exactly one URL or file ID")
				}
			case "raw":
				if part.Raw == nil {
					return fmt.Errorf("workspace: raw session part has no content")
				}
			default:
				return fmt.Errorf("workspace: unsupported session content kind %q", part.Kind)
			}
		}
	}
	return nil
}

func decodeSessionStrict(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

func toSessionMessages(messages []client.Message) []sessionMessage {
	if messages == nil {
		return nil
	}
	result := make([]sessionMessage, 0, len(messages))
	for _, message := range messages {
		saved := sessionMessage{Role: message.Role, Phase: message.Phase, Name: message.Name, ToolCallID: message.ToolCallID}
		if len(message.ContentParts) > 0 {
			for _, part := range message.ContentParts {
				saved.Content = append(saved.Content, toSessionPart(part))
			}
		} else if message.Content != "" {
			saved.Content = []sessionPart{{Kind: "text", Text: message.Content}}
		}
		for _, call := range message.ToolCalls {
			saved.ToolCalls = append(saved.ToolCalls, sessionToolCall{
				Index: call.Index, ID: call.ID, Type: call.Type,
				Name: call.Function.Name, Arguments: call.Function.Arguments,
			})
		}
		result = append(result, saved)
	}
	return result
}

func toSessionPart(part client.MessageContentPart) sessionPart {
	typeName, _ := part["type"].(string)
	switch typeName {
	case "text", "input_text", "output_text":
		if value, ok := part["text"].(string); ok {
			return sessionPart{Kind: "text", Text: value, Extra: sessionPartExtra(part, "type", "text")}
		}
	case "image_url", "input_image":
		if image, ok := part["image_url"].(map[string]any); ok {
			url, _ := image["url"].(string)
			detail, _ := image["detail"].(string)
			if len(image) <= 2 && url != "" {
				return sessionPart{Kind: "image", URL: url, Detail: detail, Extra: sessionPartExtra(part, "type", "image_url")}
			}
		}
		if url, ok := part["image_url"].(string); ok && url != "" {
			detail, _ := part["detail"].(string)
			return sessionPart{Kind: "image", URL: url, Detail: detail, Extra: sessionPartExtra(part, "type", "image_url", "detail")}
		}
		if fileID, ok := part["file_id"].(string); ok && fileID != "" {
			detail, _ := part["detail"].(string)
			return sessionPart{Kind: "image", FileID: fileID, Detail: detail, Extra: sessionPartExtra(part, "type", "file_id", "detail")}
		}
	}
	return sessionPart{Kind: "raw", Raw: part}
}

func sessionPartExtra(part client.MessageContentPart, excluded ...string) map[string]any {
	extra := make(map[string]any)
	for key, value := range part {
		include := true
		for _, skip := range excluded {
			if key == skip {
				include = false
				break
			}
		}
		if include {
			extra[key] = value
		}
	}
	if len(extra) == 0 {
		return nil
	}
	return extra
}

func fromSessionMessages(messages []sessionMessage) []client.Message {
	if messages == nil {
		return nil
	}
	result := make([]client.Message, 0, len(messages))
	for _, saved := range messages {
		message := client.Message{Role: saved.Role, Phase: saved.Phase, Name: saved.Name, ToolCallID: saved.ToolCallID}
		if len(saved.Content) == 1 && saved.Content[0].Kind == "text" && len(saved.Content[0].Extra) == 0 {
			message.Content = saved.Content[0].Text
		} else {
			for _, part := range saved.Content {
				message.ContentParts = append(message.ContentParts, fromSessionPart(part))
			}
		}
		for _, call := range saved.ToolCalls {
			message.ToolCalls = append(message.ToolCalls, client.ToolCall{
				Index: call.Index, ID: call.ID, Type: call.Type,
				Function: client.FunctionCall{Name: call.Name, Arguments: call.Arguments},
			})
		}
		result = append(result, message)
	}
	return result
}

func fromSessionPart(part sessionPart) client.MessageContentPart {
	if part.Kind == "raw" {
		return part.Raw
	}
	result := make(client.MessageContentPart, len(part.Extra)+2)
	for key, value := range part.Extra {
		result[key] = value
	}
	switch part.Kind {
	case "text":
		result["type"] = "text"
		result["text"] = part.Text
	case "image":
		if part.FileID != "" {
			result["type"] = "input_image"
			result["file_id"] = part.FileID
			if part.Detail != "" {
				result["detail"] = part.Detail
			}
		} else {
			image := map[string]any{"url": part.URL}
			if part.Detail != "" {
				image["detail"] = part.Detail
			}
			result["type"] = "image_url"
			result["image_url"] = image
		}
	}
	return result
}
