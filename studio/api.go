package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const maximumSettingsRequestSize = 64 << 10

type apiError struct {
	Error string `json:"error"`
}

func decodeSettingsRequest(writer http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maximumSettingsRequestSize)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode settings: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode settings: multiple JSON values")
		}
		return fmt.Errorf("decode settings: %w", err)
	}
	return nil
}

func writeAPIError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, apiError{Error: err.Error()})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
