package client

import (
	"encoding/json"
	"strings"
)

func incompleteResponse(document responseDocument, affinity, model string, raw json.RawMessage) *IncompleteGeneration {
	message := Message{Role: RoleAssistant}
	for _, raw := range document.Output {
		var item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		if item.Type == "function_call" {
			message.ToolCalls = append(message.ToolCalls, ToolCall{ID: item.CallID, Type: ToolTypeFunction, Function: FunctionCall{Name: item.Name, Arguments: item.Arguments}})
		}
		if item.Type == "message" {
			for _, part := range item.Content {
				if part.Type == "output_text" {
					message.Content += part.Text
				}
			}
		}
	}
	reason := "incomplete"
	if document.IncompleteDetails != nil {
		reason = document.IncompleteDetails.Reason
	}
	if reason == "max_output_tokens" {
		reason = "output_limit"
	}
	limit := 0
	if document.MaxOutputTokens != nil {
		limit = *document.MaxOutputTokens
	}
	response := &ChatResponse{ID: document.ID, Model: model, ConversationID: affinity, Choices: []Choice{{Message: message, FinishReason: "length"}}, Usage: Usage{PromptTokens: document.Usage.InputTokens, CompletionTokens: document.Usage.OutputTokens, TotalTokens: document.Usage.TotalTokens, PromptDetails: &TokenDetails{CachedTokens: document.Usage.InputDetails.CachedTokens, CacheWriteTokens: document.Usage.InputDetails.CacheWriteTokens}}}
	response.Raw = append(json.RawMessage(nil), raw...)
	var presence struct {
		Usage struct {
			InputDetails json.RawMessage `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &presence) != nil || len(presence.Usage.InputDetails) == 0 || string(presence.Usage.InputDetails) == "null" {
		response.Usage.PromptDetails = nil
	}
	return &IncompleteGeneration{Reason: reason, Response: response, Limit: limit, Cause: responseDiagnostic(responseFailureMessage(document))}
}

type responseDiagnostic string

func (e responseDiagnostic) Error() string { return string(e) }

func isContextLimit(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "context_length_exceeded") || strings.Contains(text, "maximum context length") || strings.Contains(text, "prompt is too long")
}
