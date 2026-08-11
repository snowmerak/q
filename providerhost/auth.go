package providerhost

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const ChildAPIKeyEnv = "Q_GATEWAY_CHILD_API_KEY"

func NewEphemeralAPIKey() (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("providerhost: generate Gateway API key: %w", err)
	}
	return "qgw_" + base64.RawURLEncoding.EncodeToString(random), nil
}

func AuthenticatedHandler(apiKey string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		scheme, credential, found := strings.Cut(request.Header.Get("Authorization"), " ")
		authorized := found && strings.EqualFold(scheme, "Bearer") &&
			subtle.ConstantTimeCompare([]byte(credential), []byte(apiKey)) == 1
		if apiKey == "" || !authorized {
			writer.Header().Set("Content-Type", "application/json")
			writer.Header().Set("WWW-Authenticate", "Bearer")
			writer.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{
				"message": "invalid Gateway API key",
				"type":    "authentication_error",
				"code":    "invalid_api_key",
			}})
			return
		}
		next.ServeHTTP(writer, request)
	})
}
