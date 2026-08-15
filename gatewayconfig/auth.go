package gatewayconfig

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

type Authenticator struct {
	masterKey [32]byte
	mu        sync.RWMutex
	keys      map[string]APIKey
}

func NewAuthenticator(masterKey [32]byte, value Config) (*Authenticator, error) {
	authenticator := &Authenticator{}
	if err := authenticator.ReloadWithMasterKey(masterKey, value); err != nil {
		return nil, err
	}
	return authenticator, nil
}

func (a *Authenticator) Reload(value Config) error {
	keys, err := activeAPIKeys(value)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.keys = keys
	a.mu.Unlock()
	return nil
}

// ReloadWithMasterKey atomically replaces both the verification key and API
// key records. It is used when authentication becomes enabled after a Gateway
// started without configured keys.
func (a *Authenticator) ReloadWithMasterKey(masterKey [32]byte, value Config) error {
	keys, err := activeAPIKeys(value)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.masterKey = masterKey
	a.keys = keys
	a.mu.Unlock()
	return nil
}

func activeAPIKeys(value Config) (map[string]APIKey, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	keys := make(map[string]APIKey, value.ActiveKeyCount())
	for _, key := range value.APIKeys {
		if key.RevokedAt == nil {
			keys[key.ID] = key
		}
	}
	return keys, nil
}

func (a *Authenticator) Authorized(authorization string) bool {
	scheme, presented, found := strings.Cut(authorization, " ")
	validScheme := found && strings.EqualFold(scheme, "Bearer")
	id, _, ok := ParseAPIKey(presented)
	a.mu.RLock()
	record, found := a.keys[id]
	masterKey := a.masterKey
	a.mu.RUnlock()
	return validScheme && ok && found && VerifyAPIKey(masterKey, record, presented)
}

func (a *Authenticator) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !a.Authorized(request.Header.Get("Authorization")) {
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

// OptionalHandler disables authentication while there are no active API keys.
// Once keys are loaded, it applies the same Bearer authentication as Handler.
func (a *Authenticator) OptionalHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization := request.Header.Get("Authorization")
		scheme, presented, hasCredentials := strings.Cut(authorization, " ")
		validScheme := hasCredentials && strings.EqualFold(scheme, "Bearer")
		id, _, validKey := ParseAPIKey(presented)
		a.mu.RLock()
		disabled := len(a.keys) == 0
		record, found := a.keys[id]
		masterKey := a.masterKey
		a.mu.RUnlock()
		if disabled || validScheme && validKey && found && VerifyAPIKey(masterKey, record, presented) {
			next.ServeHTTP(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("WWW-Authenticate", "Bearer")
		writer.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{
			"message": "invalid Gateway API key",
			"type":    "authentication_error",
			"code":    "invalid_api_key",
		}})
	})
}
