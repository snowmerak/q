package anthropic

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	llmprovider "github.com/snowmerak/q/v2/pkg/llm"
)

type sseStream struct {
	body    io.ReadCloser
	reader  *bufio.Reader
	headers http.Header

	mu      sync.Mutex
	done    bool
	id      string
	model   string
	usage   usageResponse
	toolIDs map[int]string
	tools   map[int]string
	inputs  map[int]string
	hasJSON map[int]bool
}

func newSSEStream(body io.ReadCloser, headers http.Header) *sseStream {
	return &sseStream{
		body: body, reader: bufio.NewReader(body), headers: headers,
		toolIDs: make(map[int]string), tools: make(map[int]string),
		inputs: make(map[int]string), hasJSON: make(map[int]bool),
	}
}

func (s *sseStream) ResponseHeaders() http.Header { return s.headers.Clone() }

func (s *sseStream) Recv() (*llmprovider.ChatChunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return nil, io.EOF
	}
	for {
		event, data, err := readSSEEvent(s.reader)
		if err != nil {
			s.done = true
			_ = s.body.Close()
			return nil, err
		}
		switch event {
		case "message_start":
			var value struct {
				Message struct {
					ID    string        `json:"id"`
					Model string        `json:"model"`
					Usage usageResponse `json:"usage"`
				} `json:"message"`
			}
			if err := json.Unmarshal(data, &value); err != nil {
				return nil, fmt.Errorf("anthropic: decode message_start: %w", err)
			}
			s.id, s.model, s.usage = value.Message.ID, value.Message.Model, value.Message.Usage
		case "content_block_start":
			var value struct {
				Index        int           `json:"index"`
				ContentBlock responseBlock `json:"content_block"`
			}
			if err := json.Unmarshal(data, &value); err != nil {
				return nil, fmt.Errorf("anthropic: decode content_block_start: %w", err)
			}
			if value.ContentBlock.Type != "tool_use" {
				continue
			}
			s.toolIDs[value.Index], s.tools[value.Index] = value.ContentBlock.ID, value.ContentBlock.Name
			s.inputs[value.Index] = string(value.ContentBlock.Input)
			return s.chunk(&llmprovider.Message{
				Role: llmprovider.RoleAssistant,
				ToolCalls: []llmprovider.ToolCall{{
					Index: value.Index, ID: value.ContentBlock.ID, Type: llmprovider.ToolTypeFunction,
					Function: llmprovider.FunctionCall{Name: value.ContentBlock.Name},
				}},
			}, "", nil), nil
		case "content_block_delta":
			var value struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if err := json.Unmarshal(data, &value); err != nil {
				return nil, fmt.Errorf("anthropic: decode content_block_delta: %w", err)
			}
			switch value.Delta.Type {
			case "text_delta":
				return s.chunk(&llmprovider.Message{Role: llmprovider.RoleAssistant, Content: value.Delta.Text}, "", nil), nil
			case "input_json_delta":
				if strings.TrimSpace(value.Delta.PartialJSON) != "" {
					s.hasJSON[value.Index] = true
				}
				return s.chunk(&llmprovider.Message{
					Role: llmprovider.RoleAssistant,
					ToolCalls: []llmprovider.ToolCall{{
						Index:    value.Index,
						Function: llmprovider.FunctionCall{Arguments: value.Delta.PartialJSON},
					}},
				}, "", nil), nil
			}
		case "content_block_stop":
			var value struct {
				Index int `json:"index"`
			}
			if err := json.Unmarshal(data, &value); err != nil {
				return nil, fmt.Errorf("anthropic: decode content_block_stop: %w", err)
			}
			if _, tool := s.tools[value.Index]; !tool || s.hasJSON[value.Index] {
				continue
			}
			// Empty-argument tools can send only an empty JSON delta. Use the
			// initial input after the block finishes, never before a truncated call.
			arguments := s.inputs[value.Index]
			if arguments == "" || arguments == "null" {
				arguments = "{}"
			}
			s.hasJSON[value.Index] = true
			return s.chunk(&llmprovider.Message{Role: llmprovider.RoleAssistant,
				ToolCalls: []llmprovider.ToolCall{{Index: value.Index, Function: llmprovider.FunctionCall{Arguments: arguments}}},
			}, "", nil), nil
		case "message_delta":
			var value struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage usageResponse `json:"usage"`
			}
			if err := json.Unmarshal(data, &value); err != nil {
				return nil, fmt.Errorf("anthropic: decode message_delta: %w", err)
			}
			s.usage.OutputTokens = value.Usage.OutputTokens
			usage := normalizeUsage(s.usage)
			return s.chunk(&llmprovider.Message{Role: llmprovider.RoleAssistant}, finishReason(value.Delta.StopReason), &usage), nil
		case "message_stop":
			s.done = true
			_ = s.body.Close()
			return nil, io.EOF
		case "error":
			var value struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(data, &value)
			return nil, errors.New("anthropic: stream error: " + value.Error.Message)
		}
	}
}

func (s *sseStream) chunk(delta *llmprovider.Message, reason string, usage *llmprovider.Usage) *llmprovider.ChatChunk {
	return &llmprovider.ChatChunk{
		ID: s.id, Object: "chat.completion.chunk", Model: s.model, Usage: usage,
		Choices: []llmprovider.Choice{{Index: 0, Delta: delta, FinishReason: reason}},
	}
}

func (s *sseStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
	return s.body.Close()
}

func readSSEEvent(reader *bufio.Reader) (string, []byte, error) {
	var event string
	var data []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return "", nil, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			return event, []byte(strings.Join(data, "\n")), nil
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if err != nil {
			return event, []byte(strings.Join(data, "\n")), err
		}
	}
}

var _ llmprovider.Stream = (*sseStream)(nil)
var _ llmprovider.ResponseHeaderer = (*sseStream)(nil)
