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
	authenticator := &Authenticator{masterKey: masterKey}
	if err := authenticator.Reload(value); err != nil {
		return nil, err
	}
	return authenticator, nil
}

func (a *Authenticator) Reload(value Config) error {
	if err := value.Validate(); err != nil {
		return err
	}
	keys := make(map[string]APIKey, value.ActiveKeyCount())
	for _, key := range value.APIKeys {
		if key.RevokedAt == nil {
			keys[key.ID] = key
		}
	}
	a.mu.Lock()
	a.keys = keys
	a.mu.Unlock()
	return nil
}

func (a *Authenticator) Authorized(authorization string) bool {
	scheme, presented, found := strings.Cut(authorization, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	id, _, ok := ParseAPIKey(presented)
	if !ok {
		return false
	}
	a.mu.RLock()
	record, found := a.keys[id]
	a.mu.RUnlock()
	return found && VerifyAPIKey(a.masterKey, record, presented)
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
