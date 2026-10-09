// Package strictjson decodes one JSON value without unknown fields or trailing values.
package strictjson

import (
	"encoding/json"
	"errors"
	"io"
)

var ErrMultipleValues = errors.New("multiple JSON values")

// Decode leaves input size limits and reader ownership with the caller.
func Decode(input io.Reader, target any) error {
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return ErrMultipleValues
		}
		return err
	}
	return nil
}
