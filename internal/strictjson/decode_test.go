package strictjson

import (
	"errors"
	"strings"
	"testing"
)

func TestDecodeRejectsUnknownAndTrailingValues(t *testing.T) {
	for _, test := range []struct {
		input    string
		valid    bool
		multiple bool
	}{
		{input: `{"value":1} `, valid: true},
		{input: `{"value":1,"extra":2}`},
		{input: `{"value":1} {}`, multiple: true},
		{input: `{"value":1} invalid`},
		{input: ""},
	} {
		t.Run(test.input, func(t *testing.T) {
			var value struct {
				Value int `json:"value"`
			}
			err := Decode(strings.NewReader(test.input), &value)
			if (err == nil) != test.valid || errors.Is(err, ErrMultipleValues) != test.multiple {
				t.Fatalf("decode error = %v", err)
			}
		})
	}
}
