package diagnostics

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRequestIDPreservesValidAndGeneratesMissingOrInvalid(t *testing.T) {
	id := uuid.NewString()
	for _, test := range []struct {
		name, input, want string
	}{
		{"supplied", id, id},
		{"uppercase", strings.ToUpper(id), id},
		{"missing", "", ""},
		{"invalid", "private-untrusted-value", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := WithRequestID(t.Context(), test.input)
			got := RequestID(ctx)
			if parsed, err := uuid.Parse(got); err != nil || parsed.String() != got {
				t.Fatalf("not a canonical UUID: %q", got)
			}
			if test.want != "" && got != test.want {
				t.Fatalf("request ID = %q, want %q", got, test.want)
			}
			if RequestID(WithRequestID(ctx, "")) != got {
				t.Fatal("nested scope replaced existing ID")
			}
		})
	}
	first := RequestID(WithRequestID(t.Context(), ""))
	second := RequestID(WithRequestID(t.Context(), ""))
	if first == second {
		t.Fatal("independent requests reused an ID")
	}
}

func TestScopedAttrsUseCurrentRequestIDWithoutLeakingToSibling(t *testing.T) {
	var output bytes.Buffer
	parent := WithRequestID(WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&output, nil))), "")
	child := WithAttrs(parent, slog.String("seat", "A"))
	child = WithRequestID(child, uuid.NewString())
	Logger(child).Info("child")
	Logger(parent).Info("parent")
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	for index, ctx := range []struct{ id, seat string }{{RequestID(child), "A"}, {RequestID(parent), ""}} {
		var record map[string]any
		if err := json.Unmarshal(lines[index], &record); err != nil {
			t.Fatal(err)
		}
		if record["request_id"] != ctx.id || bytes.Count(lines[index], []byte(`"request_id"`)) != 1 {
			t.Fatalf("wrong or duplicated correlation: %s", lines[index])
		}
		seat, _ := record["seat"].(string)
		if seat != ctx.seat {
			t.Fatalf("scope leaked: %s", lines[index])
		}
	}
}
