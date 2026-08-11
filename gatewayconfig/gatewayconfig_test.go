package gatewayconfig

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStoreRoundTripAndMasterKey(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	loaded, err := store.LoadOrDefault()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Server.Host != DefaultHost || loaded.Server.Port != DefaultPort {
		t.Fatalf("defaults = %#v", loaded.Server)
	}
	loaded.Server.Port = 18181
	if err := store.Save(loaded); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Server.Port != 18181 {
		t.Fatalf("reloaded server = %#v", reloaded.Server)
	}

	var expected [32]byte
	for index := range expected {
		expected[index] = byte(index + 1)
	}
	created, err := store.EnsureMasterKey(expected)
	if err != nil {
		t.Fatal(err)
	}
	if created != expected {
		t.Fatalf("created master key differs")
	}
	var replacement [32]byte
	replacement[0] = 99
	preserved, err := store.EnsureMasterKey(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if preserved != expected {
		t.Fatal("existing master key was replaced")
	}
}

func TestGeneratedAPIKeyCanBeVerifiedAndRevoked(t *testing.T) {
	var master [32]byte
	master[0] = 42
	now := time.Date(2026, 8, 12, 1, 2, 3, 0, time.UTC)
	generated, err := GenerateAPIKey(master, "desktop", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(generated.Secret, "qk_") || strings.Contains(generated.Record.Hash, generated.Secret) {
		t.Fatalf("generated key = %#v", generated)
	}
	value, err := AddAPIKey(Default(), generated)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyAPIKey(master, value.APIKeys[0], generated.Secret) || VerifyAPIKey(master, value.APIKeys[0], generated.Secret+"wrong") {
		t.Fatal("API key verification mismatch")
	}
	revoked, err := RevokeAPIKey(value, generated.Record.ID, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if VerifyAPIKey(master, revoked.APIKeys[0], generated.Secret) || revoked.ActiveKeyCount() != 0 {
		t.Fatal("revoked API key remained active")
	}
}

func TestAuthenticatorReloadAppliesRevocation(t *testing.T) {
	var master [32]byte
	master[0] = 7
	generated, err := GenerateAPIKey(master, "automation", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	value, err := AddAPIKey(Default(), generated)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewAuthenticator(master, value)
	if err != nil {
		t.Fatal(err)
	}
	handler := authenticator.Handler(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+generated.Secret)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("authorized status = %d", response.Code)
	}

	revoked, err := RevokeAPIKey(value, generated.Record.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := authenticator.Reload(revoked); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked status = %d", response.Code)
	}
}

func TestAPIKeyAliasesAreUnique(t *testing.T) {
	var master [32]byte
	first, err := GenerateAPIKey(master, "Desktop", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	value, err := AddAPIKey(Default(), first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := GenerateAPIKey(master, "desktop", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AddAPIKey(value, second); err == nil {
		t.Fatal("duplicate alias was accepted")
	}
}
