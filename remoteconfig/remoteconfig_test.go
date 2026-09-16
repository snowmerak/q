package remoteconfig

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDefaultIsLoopbackAndUnauthenticated(t *testing.T) {
	value := Default()
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	if value.Server.Host != "127.0.0.1" || value.Server.Port != 0 || value.Authentication.Enabled {
		t.Fatalf("default = %#v", value)
	}
	if !value.ServerIsLoopback() {
		t.Fatal("default server is not loopback")
	}
}

func TestAuthenticationRequiresAnActiveRemoteKey(t *testing.T) {
	value := Default()
	value.Authentication.Enabled = true
	if err := value.Validate(); err == nil || !strings.Contains(err.Error(), "at least one active") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestRemoteKeysUseIndependentPrefixAndDomain(t *testing.T) {
	master := [32]byte{1, 2, 3}
	generated, err := GenerateAPIKey(master, "automation", time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(generated.Secret, "qrk_") {
		t.Fatalf("secret = %q", generated.Secret)
	}
	if !VerifyAPIKey(master, generated.Record, generated.Secret) {
		t.Fatal("generated key was not verified")
	}
	if VerifyAPIKey(master, generated.Record, strings.Replace(generated.Secret, "qrk_", "qk_", 1)) {
		t.Fatal("Gateway-prefixed key was accepted")
	}
}

func TestStoreKeyLifecycleProtectsEnabledAuthentication(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	value, generated, err := store.CreateAPIKey(Default(), "client", time.Unix(20, 0))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mustRead(t, store.Path())), generated.Secret) {
		t.Fatal("stored settings contain the plaintext secret")
	}
	value.Authentication.Enabled = true
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeAPIKey(value, generated.Record.ID, time.Unix(30, 0)); err == nil {
		t.Fatal("revoked the final key while authentication was enabled")
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ActiveKeyCount() != 1 || !loaded.Authentication.Enabled {
		t.Fatalf("stored config changed after rejected revoke: %#v", loaded)
	}
}

func TestStoreRejectsTrailingJSON(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	body, err := json.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), append(body, []byte("\n{}")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("load error = %v", err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
