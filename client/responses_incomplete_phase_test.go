package client

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestIncompleteResponsesExcludeCommentaryFromContinuation(t *testing.T) {
	const progress = "I will check the sources first."
	const answer = "The answer is "
	partial := json.RawMessage(`{"id":"partial","model":"test","status":"incomplete","max_output_tokens":96,"incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","phase":"commentary","content":[{"type":"output_text","text":"I will check the sources first."}]},{"type":"message","phase":"final_answer","content":[{"type":"output_text","text":"The answer is "}]}]}`)
	for _, commentaryOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial answer", true: "progress only"}[commentaryOnly], func(t *testing.T) {
			body := partial
			if commentaryOnly {
				var document map[string]json.RawMessage
				if err := json.Unmarshal(partial, &document); err != nil {
					t.Fatal(err)
				}
				var output []json.RawMessage
				if err := json.Unmarshal(document["output"], &output); err != nil {
					t.Fatal(err)
				}
				document["output"], _ = json.Marshal(output[:1])
				body, _ = json.Marshal(document)
			}
			_, parseErr := parseResponseDocument(body, "", "test")
			var incomplete *IncompleteGeneration
			if !errors.As(parseErr, &incomplete) {
				t.Fatalf("expected incomplete response: %v", parseErr)
			}
			message := incomplete.Response.Choices[0].Message
			if strings.Contains(message.TextContent(), progress) {
				t.Fatal("progress leaked into partial answer")
			}
			if !commentaryOnly && (message.Content != answer || message.Phase != "final_answer") {
				t.Fatalf("partial answer metadata lost: %+v", message)
			}
			calls := 0
			ctx := WithGenerationSettings(t.Context(), GenerationSettings{SafeReplay: true, MaxAttempts: 1})
			response, err := RecoverGeneration(ctx, nil, ChatRequest{Model: "test"}, func(_ context.Context, request ChatRequest) (*ChatResponse, error) {
				calls++
				if calls == 1 {
					return nil, parseErr
				}
				for _, message := range request.Messages {
					if strings.Contains(message.TextContent(), progress) {
						t.Fatal("progress sent as answer continuation")
					}
				}
				return generationResponse("42.", "stop", 2), nil
			})
			if commentaryOnly {
				if err == nil || calls != 1 {
					t.Fatalf("progress was treated as an unfinished answer: calls=%d err=%v", calls, err)
				}
			} else if err != nil || response.Choices[0].Message.Content != "The answer is 42." || calls != 2 {
				t.Fatalf("continuation=%+v calls=%d err=%v", response, calls, err)
			}
		})
	}
}
