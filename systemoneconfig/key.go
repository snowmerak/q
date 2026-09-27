package systemoneconfig

import (
	"crypto/rand"
	"fmt"
	"os"
	"time"

	"github.com/snowmerak/q/internal/authkey"
)

var apiKeyPolicy = authkey.Policy{
	Name:         "systemone",
	SecretPrefix: "qso_",
	Domain:       "q.systemone.api-key.v1\x00",
}

type GeneratedAPIKey = authkey.Generated

func ValidateAlias(alias string) error {
	return authkey.ValidateAlias(alias, apiKeyPolicy.Name)
}

func (c Config) ActiveKeyCount() int {
	count := authkey.ActiveCount(c.APIKeys)
	if c.Server.APIKey != "" {
		count++
	}
	return count
}

func (c Config) ActiveGeneratedKeyCount() int {
	return authkey.ActiveCount(c.APIKeys)
}

func (s Store) LoadMasterKey() ([32]byte, error) {
	return authkey.LoadMasterKey(s.MasterKeyPath(), apiKeyPolicy.Name)
}

func (s Store) EnsureMasterKey(random [32]byte) ([32]byte, error) {
	return authkey.EnsureMasterKey(s.MasterKeyPath(), apiKeyPolicy.Name, random, func() error {
		if err := os.MkdirAll(s.Dir, 0o700); err != nil {
			return err
		}
		return os.Chmod(s.Dir, 0o700)
	})
}

func (s Store) CreateAPIKey(value Config, alias string, now time.Time) (Config, GeneratedAPIKey, error) {
	var candidate [32]byte
	if _, err := rand.Read(candidate[:]); err != nil {
		return Config{}, GeneratedAPIKey{}, fmt.Errorf("systemone: generate master key: %w", err)
	}
	masterKey, err := s.EnsureMasterKey(candidate)
	if err != nil {
		return Config{}, GeneratedAPIKey{}, err
	}
	generated, err := authkey.Generate(masterKey, alias, now, apiKeyPolicy)
	if err != nil {
		return Config{}, GeneratedAPIKey{}, err
	}
	keys, err := authkey.Add(value.APIKeys, generated, apiKeyPolicy.Name)
	if err != nil {
		return Config{}, GeneratedAPIKey{}, err
	}
	value.APIKeys = keys
	if err := s.Save(value); err != nil {
		return Config{}, GeneratedAPIKey{}, err
	}
	return value, generated, nil
}

func (s Store) RevokeAPIKey(value Config, id string, now time.Time) (Config, error) {
	if id == "legacy" {
		if value.Server.APIKey == "" {
			return Config{}, fmt.Errorf("systemone: legacy API key is not active")
		}
		value.Server.APIKey = ""
	} else {
		keys, err := authkey.Revoke(append([]APIKey(nil), value.APIKeys...), id, now, apiKeyPolicy.Name)
		if err != nil {
			return Config{}, err
		}
		value.APIKeys = keys
	}
	if err := s.Save(value); err != nil {
		return Config{}, err
	}
	return value, nil
}
