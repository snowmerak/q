package acp

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Keep the fork's scope patch on the wire after every upstream/schema refresh.
// In particular, a zero RequestId union cannot be marshaled, so an absent
// request-scoped ID must be nil/omitted for session-scoped forms and URLs.
func TestUnstableCreateElicitationRequest_ScopeRoundTrip(t *testing.T) {
	for _, mode := range []string{"form", "url"} {
		for _, scope := range []struct {
			name string
			json string
		}{
			{"session", `"sessionId":"session-1"`},
			{"session tool", `"sessionId":"session-1","toolCallId":"tool-2"`},
			{"numeric request", `"requestId":12`},
			{"zero request", `"requestId":0`},
			{"string request", `"requestId":"initialize-1"`},
		} {
			t.Run(mode+"/"+scope.name, func(t *testing.T) {
				fields := `"requestedSchema":{"type":"object","properties":{}}`
				if mode == "url" {
					fields = `"elicitationId":"elicitation-1","url":"https://example.com/auth"`
				}
				input := []byte(`{"mode":"` + mode + `","message":"Input needed",` + fields + `,` + scope.json + `}`)
				var request UnstableCreateElicitationRequest
				if err := json.Unmarshal(input, &request); err != nil {
					t.Fatal(err)
				}
				if err := request.Validate(); err != nil {
					t.Fatal(err)
				}
				output, err := json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				var want, got map[string]any
				if err := json.Unmarshal(input, &want); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(output, &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("wire scope changed:\n got %s\nwant %s", output, input)
				}
			})
		}
	}
}

func TestCancelNotification_PreservesRequiredSessionID(t *testing.T) {
	for _, id := range []SessionId{"", "session-1"} {
		data, err := json.Marshal(CancelNotification{SessionId: id})
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		if _, ok := fields["sessionId"]; !ok {
			t.Fatalf("required sessionId was omitted: %s", data)
		}
	}
}
