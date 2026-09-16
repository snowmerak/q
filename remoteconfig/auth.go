package remoteconfig

import (
	"strings"
	"sync"
)

type Authenticator struct {
	mu        sync.RWMutex
	enabled   bool
	masterKey [32]byte
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
	a.enabled = value.Authentication.Enabled
	a.keys = keys
	a.mu.Unlock()
	return nil
}

func (a *Authenticator) ReloadWithMasterKey(masterKey [32]byte, value Config) error {
	keys, err := activeAPIKeys(value)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.enabled = value.Authentication.Enabled
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

func (a *Authenticator) Enabled() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.enabled
}

func (a *Authenticator) Authorized(authorization string) bool {
	scheme, presented, found := strings.Cut(authorization, " ")
	id, _, validKey := ParseAPIKey(presented)
	a.mu.RLock()
	if !a.enabled {
		a.mu.RUnlock()
		return true
	}
	record, known := a.keys[id]
	masterKey := a.masterKey
	a.mu.RUnlock()
	return found && strings.EqualFold(scheme, "Bearer") && validKey && known && VerifyAPIKey(masterKey, record, presented)
}
