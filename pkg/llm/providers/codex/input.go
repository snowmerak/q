package codex

import (
	"fmt"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

// turnInput preserves the order of text and images in a user message.
func turnInput(message llmprovider.Message) ([]map[string]any, error) {
	if len(message.ContentParts) == 0 {
		return []map[string]any{{"type": "text", "text": message.Content}}, nil
	}

	input := make([]map[string]any, 0, len(message.ContentParts))
	for index, part := range message.ContentParts {
		typeName, _ := part["type"].(string)
		switch typeName {
		case "text", "input_text", "output_text":
			value, ok := part["text"].(string)
			if !ok {
				return nil, fmt.Errorf("codex: content part %d has no text", index)
			}
			input = append(input, map[string]any{"type": "text", "text": value})
		case "image_url", "input_image":
			image, err := imageInput(part)
			if err != nil {
				return nil, fmt.Errorf("codex: content part %d: %w", index, err)
			}
			input = append(input, image)
		default:
			return nil, fmt.Errorf("codex: unsupported content part %d type %q", index, typeName)
		}
	}
	return input, nil
}

func imageInput(part llmprovider.MessageContentPart) (map[string]any, error) {
	input := map[string]any{"type": "image"}
	var detail any
	switch part["type"] {
	case "image_url":
		switch value := part["image_url"].(type) {
		case string:
			input["url"] = value
		case map[string]any:
			input["url"] = value["url"]
			detail = value["detail"]
		}
	case "input_image":
		if fileID, ok := part["file_id"].(string); ok && fileID != "" {
			input["fileId"] = fileID
		} else {
			input["url"] = part["image_url"]
		}
	}
	if _, hasFileID := input["fileId"]; !hasFileID {
		url, ok := input["url"].(string)
		if !ok || url == "" {
			return nil, fmt.Errorf("image URL is required")
		}
	}
	if value, ok := part["detail"]; ok {
		detail = value
	}
	if detail != nil {
		value, ok := detail.(string)
		if !ok || (value != "auto" && value != "low" && value != "high" && value != "original") {
			return nil, fmt.Errorf("unsupported image detail %v", detail)
		}
		input["detail"] = value
	}
	return input, nil
}

func historyContent(message llmprovider.Message) ([]map[string]any, error) {
	input, err := turnInput(message)
	if err != nil {
		return nil, err
	}
	content := make([]map[string]any, 0, len(input))
	for _, item := range input {
		switch item["type"] {
		case "text":
			text := item["text"].(string)
			if text == "" {
				continue
			}
			contentType := "input_text"
			if message.Role == llmprovider.RoleAssistant {
				contentType = "output_text"
			}
			content = append(content, map[string]any{"type": contentType, "text": text})
		case "image":
			if message.Role != llmprovider.RoleUser {
				return nil, fmt.Errorf("codex: image history requires a user message")
			}
			image := map[string]any{"type": "input_image"}
			if url, ok := item["url"]; ok {
				image["image_url"] = url
			} else {
				image["file_id"] = item["fileId"]
			}
			if detail, ok := item["detail"]; ok {
				image["detail"] = detail
			}
			content = append(content, image)
		}
	}
	return content, nil
}
