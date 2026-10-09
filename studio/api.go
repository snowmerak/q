package studio

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/snowmerak/q/internal/strictjson"
)

const maximumSettingsRequestSize = 64 << 10

type apiError struct {
	Error string `json:"error"`
}

func decodeSettingsRequest(writer http.ResponseWriter, request *http.Request, target any) error {
	return decodeRequest(writer, request, target, maximumSettingsRequestSize, "decode settings")
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

func decodeRequest(writer http.ResponseWriter, request *http.Request, target any, maximumSize int64, label string) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maximumSize)
	if err := strictjson.Decode(request.Body, target); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}
