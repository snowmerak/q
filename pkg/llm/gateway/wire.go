package gateway

import "encoding/json"

func rewriteTopLevelModel(data []byte, model string) []byte {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return data
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		return data
	}
	object["model"] = encoded
	result, err := json.Marshal(object)
	if err != nil {
		return data
	}
	return result
}

func rewriteChatResponse(data []byte, model, conversationID string) []byte {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return data
	}
	encodedModel, err := json.Marshal(model)
	if err != nil {
		return data
	}
	object["model"] = encodedModel
	if conversationID != "" {
		encodedConversationID, encodeErr := json.Marshal(conversationID)
		if encodeErr != nil {
			return data
		}
		object["conversation_id"] = encodedConversationID
	}
	result, err := json.Marshal(object)
	if err != nil {
		return data
	}
	return result
}

func rewriteResponseEventModel(data []byte, model string) []byte {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return data
	}
	encoded, _ := json.Marshal(model)
	if _, ok := object["model"]; ok {
		object["model"] = encoded
	}
	if rawResponse, ok := object["response"]; ok {
		var response map[string]json.RawMessage
		if json.Unmarshal(rawResponse, &response) == nil {
			response["model"] = encoded
			if rewritten, err := json.Marshal(response); err == nil {
				object["response"] = rewritten
			}
		}
	}
	result, err := json.Marshal(object)
	if err != nil {
		return data
	}
	return result
}
