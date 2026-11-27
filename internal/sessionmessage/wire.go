package sessionmessage

import (
	"encoding/json"
	"fmt"

	"github.com/snowmerak/q/client"
)

const CurrentVersion = 3

type Message struct {
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

// Transcript decodes the saved conversation without loading model context,
// provider replay state, or thought history.
type Transcript struct {
	ID         string
	RunID      string
	Transcript []client.Message
}

func (s *Transcript) UnmarshalJSON(data []byte) error {
	var saved struct {
		Version    int             `json:"version"`
		ID         string          `json:"id"`
		RunID      string          `json:"run_id"`
		Transcript json.RawMessage `json:"transcript"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		return err
	}
	out := Transcript{ID: saved.ID, RunID: saved.RunID}
	if len(saved.Transcript) > 0 {
		switch saved.Version {
		case 1:
			if err := json.Unmarshal(saved.Transcript, &out.Transcript); err != nil {
				return err
			}
		case 2, CurrentVersion:
			var messages []Message
			if err := json.Unmarshal(saved.Transcript, &messages); err != nil {
				return err
			}
			out.Transcript = Decode(messages)
		default:
			return fmt.Errorf("workspace: unsupported session version %d", saved.Version)
		}
	}
	*s = out
	return nil
}

func Validate(messages []Message) error {
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

func Encode(messages []client.Message) []Message {
	if messages == nil {
		return nil
	}
	result := make([]Message, 0, len(messages))
	for _, message := range messages {
		saved := Message{Role: message.Role, Phase: message.Phase, Name: message.Name, ToolCallID: message.ToolCallID}
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

func Decode(messages []Message) []client.Message {
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
