package agentloop_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/snowmerak/q/agentloop"
	"github.com/snowmerak/q/client"
)

type completionScript struct {
	messages []client.Message
	requests []client.ChatRequest
}

func (s *completionScript) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	index := len(s.requests)
	request.Messages = append([]client.Message(nil), request.Messages...)
	s.requests = append(s.requests, request)
	if index >= len(s.messages) {
		return nil, fmt.Errorf("unexpected request %d", index+1)
	}
	message := s.messages[index]
	reason := "stop"
	if len(message.ToolCalls) > 0 {
		reason = "tool_calls"
	}
	return &client.ChatResponse{ConversationID: "retained-thread", Usage: client.Usage{PromptTokens: 100 + index, CompletionTokens: 10 + index, TotalTokens: 110 + 2*index}, Choices: []client.Choice{{Message: message, FinishReason: reason}}}, nil
}

func (s *completionScript) ChatStream(ctx context.Context, request client.ChatRequest) (client.Stream, error) {
	response, err := s.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	choice := response.Choices[0]
	return &chunkStream{chunks: []*client.ChatChunk{{ConversationID: response.ConversationID, Usage: &response.Usage, Choices: []client.Choice{{Delta: &choice.Message, FinishReason: choice.FinishReason}}}}}, nil
}

func completionCall(name string) client.Message {
	args := `{}`
	if name == "task_start" {
		args = `{"objective":"Investigate and explain"}`
	}
	if name == "task_complete" {
		args = `{"outcome":"succeeded","summary":"Investigation finished","verification":["Checked sources"]}`
	}
	return client.Message{Role: client.RoleAssistant, ToolCalls: []client.ToolCall{{ID: name, Type: client.ToolTypeFunction, Function: client.FunctionCall{Name: name, Arguments: args}}}}
}

func TestTaskCompletionSeparatesAnswerFromLifecycle(t *testing.T) {
	const answer = "## Findings\n\nThe verified result is 42.\n\nSource: https://example.com/evidence"
	native := client.Message{Role: client.RoleAssistant, Content: answer, Phase: "final_answer", ResponseModel: "test/model", ResponseOutput: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)}}
	ack := native
	ack.Content = "Acknowledged internally."
	withAnswer := completionCall("task_complete")
	withAnswer.Content = answer
	plain := client.Message{Role: client.RoleAssistant, Content: answer}
	progress := completionCall("lookup")
	progress.Content = "I will investigate now."
	for _, streaming := range []bool{false, true} {
		for _, test := range []struct {
			name              string
			messages          []client.Message
			already, reminder bool
			expected          string
			calls             int
		}{
			{"after completion", []client.Message{completionCall("task_start"), progress, completionCall("task_complete"), native}, false, false, answer, 1},
			{"before completion", []client.Message{completionCall("task_start"), completionCall("lookup"), plain, completionCall("task_complete"), ack}, true, true, answer, 1},
			{"alongside completion", []client.Message{completionCall("task_start"), completionCall("lookup"), withAnswer, ack}, true, false, answer, 1},
			{"more work invalidates earlier answer", []client.Message{completionCall("task_start"), {Role: client.RoleAssistant, Content: "Earlier answer"}, completionCall("lookup"), completionCall("task_complete"), native}, false, true, answer, 1},
			{"terminal calls rejected", []client.Message{completionCall("task_start"), completionCall("task_complete"), completionCall("lookup"), native}, false, false, answer, 0},
			{"empty terminal fallback", []client.Message{completionCall("task_start"), completionCall("task_complete"), {Role: client.RoleAssistant, ResponseModel: native.ResponseModel, ResponseOutput: native.ResponseOutput}}, false, false, "Investigation finished\n\nVerification:\n- Checked sources", 0},
			{"direct answer", []client.Message{native}, false, false, answer, 0},
		} {
			t.Run(fmt.Sprintf("stream=%v/%s", streaming, test.name), func(t *testing.T) {
				configured := &completionScript{messages: test.messages}
				runtime := &scriptedTools{name: "lookup", content: "The answer is 42"}
				events := make(chan agentloop.Event)
				go agentloop.RunAgentLoop(t.Context(), agentloop.Request{Client: configured, Tools: runtime, Model: "test/model", Stream: streaming, Messages: []client.Message{{Role: client.RoleUser, Content: "Find and explain the answer"}}}, events)
				var final agentloop.Result
				var visible, reminders []client.Message
				for event := range events {
					if err := event.Err(); err != nil {
						t.Fatal(err)
					}
					if m, ok := event.Message(); ok && m.Role == client.RoleAssistant {
						visible = append(visible, m)
					}
					if m, ok := event.ContextMessage(); ok {
						reminders = append(reminders, m)
					}
					if result, ok := event.Result(); ok {
						final = result
					}
				}
				if final.Response == nil || final.Response.Choices[0].Message.TextContent() != test.expected || final.ReplyAlreadyEmitted != test.already {
					t.Fatalf("unexpected final result: %+v", final)
				}
				if !final.ReplyAlreadyEmitted {
					visible = append(visible, final.Response.Choices[0].Message)
				}
				count := 0
				for _, m := range visible {
					if m.TextContent() == test.expected {
						count++
					}
					if strings.Contains(m.TextContent(), "Acknowledged internally") {
						t.Fatal("internal acknowledgment leaked into transcript")
					}
				}
				if count != 1 {
					t.Fatalf("answer appeared %d times", count)
				}
				if (len(reminders) > 0) != test.reminder {
					t.Fatalf("context reminders: %v", reminders)
				}
				if len(runtime.calls) != test.calls {
					t.Fatalf("executed %d tools, want %d", len(runtime.calls), test.calls)
				}
				if test.already {
					if final.ContextMessage == nil || final.ContextMessage.Content != ack.Content || !reflect.DeepEqual(final.ContextMessage.ResponseOutput, ack.ResponseOutput) {
						t.Fatalf("actual terminal context lost: %+v", final.ContextMessage)
					}
				} else if test.name != "empty terminal fallback" && !reflect.DeepEqual(final.Response.Choices[0].Message.ResponseOutput, native.ResponseOutput) {
					t.Fatal("final answer lost native replay metadata")
				}
				if test.name != "direct answer" {
					last := configured.requests[len(configured.requests)-1]
					instruction := last.Messages[len(last.Messages)-1].Content
					if strings.Contains(instruction, "Provide the final answer") == test.already {
						t.Fatalf("wrong terminal instruction: %s", instruction)
					}
					if last.ToolChoice != nil || last.ParallelToolCalls == nil || !*last.ParallelToolCalls || last.ConversationID != "retained-thread" {
						t.Fatal("terminal request changed tool settings or thread")
					}
					if final.Outcome != "succeeded" {
						t.Fatalf("task outcome lost: %q", final.Outcome)
					}
				}
				if final.Response.Usage.CompletionTokens != 9+len(test.messages) {
					t.Fatalf("terminal usage lost: %+v", final.Response.Usage)
				}
			})
		}
	}
}
