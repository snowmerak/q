package providerhost

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthenticatedHandlerRequiresMatchingBearerKey(t *testing.T) {
	handler := AuthenticatedHandler("temporary-key", http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))

	for _, authorization := range []string{"", "Bearer wrong-key", "Basic temporary-key"} {
		request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		request.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "invalid_api_key") {
			t.Fatalf("authorization %q: status=%d body=%s", authorization, response.Code, response.Body.String())
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer temporary-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("authorized status = %d", response.Code)
	}
}

func TestNewEphemeralAPIKeyProducesDistinctPrefixedKeys(t *testing.T) {
	first, err := NewEphemeralAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewEphemeralAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "qgw_") || !strings.HasPrefix(second, "qgw_") || first == second {
		t.Fatalf("generated keys = %q, %q", first, second)
	}
}
