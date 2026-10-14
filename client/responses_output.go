package client

import (
	"encoding/json"
	"errors"
	"sort"
)

// Responses streams may put their full output only in output_item.done events and
// send an empty or partial output array in response.completed. Keep the final
// per-item snapshots, including opaque reasoning, when collecting the stream.
func collectResponseOutput(body json.RawMessage, output map[int]json.RawMessage) (json.RawMessage, error) {
	if len(output) == 0 {
		return body, nil
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, err
	}
	if document == nil {
		return nil, errors.New("client: completed response is missing its body")
	}
	var terminal []json.RawMessage
	if raw := document["output"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &terminal); err != nil {
			return nil, err
		}
	}
	ids := make(map[string]bool)
	next := 0
	for index, item := range output {
		var header struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(item, &header); err != nil {
			return nil, err
		}
		if header.ID != "" {
			ids[header.ID] = true
		}
		if index >= next {
			next = index + 1
		}
	}
	for index, item := range terminal {
		var header struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(item, &header); err != nil {
			return nil, err
		}
		if header.ID != "" && ids[header.ID] {
			continue
		}
		if _, exists := output[index]; exists {
			if header.ID == "" {
				continue
			}
			index = next
			next++
		}
		output[index] = item
		if header.ID != "" {
			ids[header.ID] = true
		}
		if index >= next {
			next = index + 1
		}
	}
	indexes := make([]int, 0, len(output))
	for index := range output {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	items := make([]json.RawMessage, 0, len(indexes))
	for _, index := range indexes {
		items = append(items, output[index])
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	document["output"] = encoded
	return json.Marshal(document)
}
