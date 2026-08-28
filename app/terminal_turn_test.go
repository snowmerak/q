package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/snowmerak/llm-provider/gateway"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/thinker"
	"github.com/snowmerak/q/third_party/acp-go-sdk"
)

// Like the Codex dynamic-tool protocol, this fake parks the old turn until its
// terminal callback receives a result. A user message sent while parked gets
// the old acknowledgment instead of an answer to the new request.
type parkedTerminalClient struct {
	requests    []client.ChatRequest
	phase       int
	pending     bool
	extraCalls  bool
	terminalErr error
	streams     int
	closed      int
}

func (p *parkedTerminalClient) Chat(_ context.Context, request client.ChatRequest) (*client.ChatResponse, error) {
	request.Messages = append([]client.Message(nil), request.Messages...)
	p.requests = append(p.requests, request)
	response := terminalAcknowledgment("same-conversation")
	toolResponse := func(calls ...client.ToolCall) (*client.ChatResponse, error) {
		response.Choices[0] = client.Choice{Message: client.Message{Role: client.RoleAssistant, ToolCalls: calls}, FinishReason: "tool_calls"}
		return response, nil
	}
	switch p.phase {
	case 0:
		p.phase++
		return toolResponse(planToolCall(taskStartToolName, `{"objective":"Inspect repository"}`))
	case 1:
		p.phase++
		p.pending = true
		return toolResponse(planToolCall(taskCompleteToolName, `{"outcome":"succeeded","summary":"Repository inspected"}`))
	}
	if p.pending {
		if p.terminalErr != nil {
			return nil, p.terminalErr
		}
		if p.extraCalls {
			p.extraCalls = false
			return toolResponse(
				client.ToolCall{ID: "unexpected-write", Function: client.FunctionCall{Name: "write_file", Arguments: `{"path":"late.txt","content":"do not write"}`}},
				client.ToolCall{Index: 1, ID: "unexpected-complete", Function: client.FunctionCall{Name: taskCompleteToolName, Arguments: `{}`}},
			)
		}
		p.pending = false
		response.Choices[0].Message.Content = "Old task acknowledgment from provider"
		return response, nil
	}
	for index := len(request.Messages) - 1; index >= 0; index-- {
		if request.Messages[index].Role == client.RoleUser {
			response.Choices[0].Message.Content = "New answer: " + request.Messages[index].Content
			return response, nil
		}
	}
	return nil, errors.New("missing user request")
}

func (p *parkedTerminalClient) ChatStream(ctx context.Context, request client.ChatRequest) (client.Stream, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	p.streams++
	choice := response.Choices[0]
	return &terminalTestStream{scriptedChatStream: scriptedChatStream{chunks: []*client.ChatChunk{{
		ConversationID: response.ConversationID,
		Choices:        []client.Choice{{Delta: &choice.Message, FinishReason: choice.FinishReason}},
	}}}, owner: p}, nil
}

func (*parkedTerminalClient) ListModels(context.Context) ([]client.Model, error) { return nil, nil }
func (*parkedTerminalClient) Close() error                                       { return nil }

type terminalTestStream struct {
	scriptedChatStream
	owner *parkedTerminalClient
}

func (s *terminalTestStream) Close() error { s.owner.closed++; return nil }

func runTerminalTestTurn(t *testing.T, m model, prompt string) model {
	t.Helper()
	m.input.SetValue(prompt)
	updated, command := m.submitChat()
	m = updated.(model)
	for count := 0; m.waiting; count++ {
		if count > 100 {
			t.Fatal("turn did not finish")
		}
		updated, command = m.Update(nextAgentMessage(t, command))
		m = updated.(model)
	}
	return m
}

func assertTerminalDelivery(t *testing.T, p *parkedTerminalClient) {
	t.Helper()
	if p.pending || len(p.requests) != 3 {
		t.Fatalf("terminal callback still parked: pending=%v requests=%d", p.pending, len(p.requests))
	}
	request := p.requests[2]
	last := request.Messages[len(request.Messages)-1]
	call := p.requests[2].Messages[len(request.Messages)-2].ToolCalls[0]
	if request.ConversationID != "same-conversation" || request.ToolChoice != client.ToolChoiceNone || last.Role != client.RoleTool || last.ToolCallID != call.ID || !strings.Contains(last.Content, `"summary":"Repository inspected"`) {
		t.Fatalf("terminal result was not returned to the same provider: %#v", request)
	}
}

func TestTaskCompletionDrainsProviderBeforeNextTUIRequest(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", streaming), func(t *testing.T) {
			p := &parkedTerminalClient{}
			value := config.Default()
			value.Provider.Model = "test/model"
			m := newModel(t.Context(), config.Store{Dir: t.TempDir()}, nil)
			m.toolRuntime = &fakeAgentTools{}
			m.enterChat(value, p)
			if streaming {
				m.gatewayConfig = gateway.Config{Providers: []gateway.ProviderConfig{{ID: "test", Type: "codex", Enabled: true}}}
			}
			m = runTerminalTestTurn(t, m, "Inspect repository")
			assertTerminalDelivery(t, p)
			last := m.messages[len(m.messages)-1]
			if last.Name != thinker.TaskCompletionReplyName || last.Content != "Repository inspected" || m.activeTask != nil {
				t.Fatalf("structured completion changed: last=%#v active=%#v", last, m.activeTask)
			}
			m = runTerminalTestTurn(t, m, "Explain the Dockerfile")
			if got := m.messages[len(m.messages)-1].Content; got != "New answer: Explain the Dockerfile" {
				t.Fatalf("new request received old completion: %q", got)
			}
			if len(p.requests) != 4 || p.requests[3].ConversationID != "same-conversation" {
				t.Fatal("follow-up dropped the conversation instead of finishing the pending turn")
			}
			for _, message := range m.messages {
				if strings.Contains(message.Content, "Old task acknowledgment") {
					t.Fatal("provider acknowledgment leaked into the visible transcript")
				}
			}
			if streaming && (p.streams != 4 || p.closed != p.streams) {
				t.Fatalf("streams=%d closed=%d", p.streams, p.closed)
			}
		})
	}
}

func TestTaskCompletionDrainsProviderBeforeNextACPRequest(t *testing.T) {
	p := &parkedTerminalClient{}
	agent, store, connection := testACPAgent(t, p, &fakeAgentTools{})
	sessionID := openTestACPSession(t, agent, store.Root)
	for index, prompt := range []string{"Inspect repository", "Explain the Dockerfile"} {
		response, err := agent.Prompt(t.Context(), acp.PromptRequest{SessionId: sessionID, Prompt: []acp.ContentBlock{acp.TextBlock(prompt)}})
		if err != nil || response.StopReason != acp.StopReasonEndTurn {
			t.Fatalf("prompt=%q response=%#v err=%v", prompt, response, err)
		}
		if index == 0 {
			assertTerminalDelivery(t, p)
		}
	}
	var output string
	for _, update := range connection.snapshot() {
		if chunk := update.Update.AgentMessageChunk; chunk != nil && chunk.Content.Text != nil {
			output += chunk.Content.Text.Text
		}
	}
	if strings.Count(output, "Repository inspected") != 1 || !strings.Contains(output, "New answer: Explain the Dockerfile") || strings.Contains(output, "Old task acknowledgment") {
		t.Fatalf("ACP returned stale/duplicate completion: %q", output)
	}
}

func TestTaskCompletionRejectsExtraToolsAndPropagatesDeliveryFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%v", failed), func(t *testing.T) {
			p := &parkedTerminalClient{extraCalls: true}
			failure := errors.New("upstream unavailable")
			if failed {
				p.terminalErr = failure
			}
			runtime := &fakeAgentTools{}
			events := make(chan agentEvent)
			go streamAgentLoop(t.Context(), p, runtime, "test/model", "", []client.Message{{Role: client.RoleUser, Content: "Inspect"}}, "", nil, true, false, events)
			var final *client.ChatResponse
			var receivedErr error
			for event := range events {
				if event.response != nil {
					final = event.response
				}
				if event.err != nil {
					receivedErr = event.err
				}
				if event.streamDelta != nil && strings.Contains(event.streamDelta.Content, "Old task acknowledgment") {
					t.Error("terminal acknowledgment was streamed to the user")
				}
			}
			if len(runtime.calls) != 0 {
				t.Fatalf("executed tools after completion: %#v", runtime.calls)
			}
			if failed {
				if final != nil || !errors.Is(receivedErr, failure) {
					t.Fatalf("delivery failure reported success: final=%#v err=%v", final, receivedErr)
				}
				return
			}
			if receivedErr != nil || final == nil || p.pending || len(p.requests) != 4 {
				t.Fatalf("final=%#v err=%v pending=%v requests=%d", final, receivedErr, p.pending, len(p.requests))
			}
			messages := p.requests[3].Messages
			for index, id := range []string{"unexpected-write", "unexpected-complete"} {
				message := messages[len(messages)-2+index]
				if message.ToolCallID != id || message.Role != client.RoleTool || !strings.HasPrefix(message.Content, "Tool error:") {
					t.Fatalf("extra call did not receive rejection: %#v", message)
				}
			}
		})
	}
}
