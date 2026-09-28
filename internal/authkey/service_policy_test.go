package authkey_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/snowmerak/q/gatewayconfig"
	"github.com/snowmerak/q/systemoneconfig"
)

func TestGatewayAndSystemOneCredentialsAreNotInterchangeable(t *testing.T) {
	masterKey := [32]byte{1, 2, 3}
	gatewayKey, err := gatewayconfig.GenerateAPIKey(masterKey, "gateway", time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}

	store := systemoneconfig.Store{Dir: t.TempDir()}
	if _, err := store.EnsureMasterKey(masterKey); err != nil {
		t.Fatal(err)
	}
	value, systemOneKey, err := store.CreateAPIKey(systemoneconfig.Default(), "system-one", time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if gatewayconfig.VerifyAPIKey(masterKey, value.APIKeys[0], systemOneKey.Secret) {
		t.Fatal("Gateway accepted a System One credential")
	}

	authenticator, err := systemoneconfig.NewAuthenticator(masterKey, value)
	if err != nil {
		t.Fatal(err)
	}
	handler := authenticator.OptionalHandler(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+gatewayKey.Secret)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("System One accepted a Gateway credential: status %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+systemOneKey.Secret)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("System One rejected its own credential: status %d", response.Code)
	}
}
