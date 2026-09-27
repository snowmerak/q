package systemoneconfig

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/snowmerak/q/internal/authkey"
)

// Authenticator accepts the saved legacy key and managed keys. It allows
// unauthenticated requests while neither kind of key is active.
type Authenticator struct {
	mu        sync.RWMutex
	masterKey [32]byte
	legacyKey string
	keys      map[string]APIKey
}

func NewAuthenticator(masterKey [32]byte, value Config) (*Authenticator, error) {
	a := &Authenticator{}
	if err := a.ReloadWithMasterKey(masterKey, value); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Authenticator) ReloadWithMasterKey(masterKey [32]byte, value Config) error {
	if err := value.Validate(); err != nil {
		return err
	}
	a.mu.Lock()
	a.masterKey = masterKey
	a.legacyKey = value.Server.APIKey
	a.keys = authkey.Active(value.APIKeys)
	a.mu.Unlock()
	return nil
}

func (a *Authenticator) OptionalHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if a.allowed(request.Header.Get("Authorization")) {
			next.ServeHTTP(writer, request)
			return
		}
		writer.Header().Set("WWW-Authenticate", "Bearer")
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]string{
			"message": "invalid System One API key",
		}})
	})
}

func (a *Authenticator) allowed(authorization string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.legacyKey == "" && len(a.keys) == 0 {
		return true
	}
	scheme, presented, found := strings.Cut(authorization, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	if a.legacyKey != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(a.legacyKey)) == 1 {
		return true
	}
	id, _, ok := authkey.Parse(presented, apiKeyPolicy)
	if !ok {
		return false
	}
	record, found := a.keys[id]
	return found && authkey.Verify(a.masterKey, record, presented, apiKeyPolicy)
}
