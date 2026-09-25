package remoteconfig

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/snowmerak/q/internal/authkey"
)

var apiKeyPolicy = authkey.Policy{
	Name:         "remoteconfig",
	SecretPrefix: "qrk_",
	Domain:       "q.remote.api-key.v1\x00",
}

type GeneratedAPIKey = authkey.Generated

func GenerateAPIKey(masterKey [32]byte, alias string, now time.Time) (GeneratedAPIKey, error) {
	return authkey.Generate(masterKey, alias, now, apiKeyPolicy)
}

func HashAPIKey(masterKey [32]byte, id, secret string) string {
	return authkey.Hash(masterKey, id, secret, apiKeyPolicy)
}

func VerifyAPIKey(masterKey [32]byte, record APIKey, presented string) bool {
	return authkey.Verify(masterKey, record, presented, apiKeyPolicy)
}

func ParseAPIKey(value string) (string, string, bool) {
	return authkey.Parse(value, apiKeyPolicy)
}

func AddAPIKey(value Config, generated GeneratedAPIKey) (Config, error) {
	keys, err := authkey.Add(value.APIKeys, generated, apiKeyPolicy.Name)
	if err != nil {
		return Config{}, err
	}
	value.APIKeys = keys
	return value, value.Validate()
}

func RevokeAPIKey(value Config, id string, now time.Time) (Config, error) {
	keys, err := authkey.Revoke(value.APIKeys, id, now, apiKeyPolicy.Name)
	if err != nil {
		return Config{}, err
	}
	value.APIKeys = keys
	if err := value.Validate(); err != nil {
		return Config{}, err
	}
	return value, nil
}

func (s Store) CreateAPIKey(value Config, alias string, now time.Time) (Config, GeneratedAPIKey, error) {
	var randomMaster [32]byte
	if _, err := rand.Read(randomMaster[:]); err != nil {
		return Config{}, GeneratedAPIKey{}, fmt.Errorf("remoteconfig: generate master key: %w", err)
	}
	masterKey, err := s.EnsureMasterKey(randomMaster)
	if err != nil {
		return Config{}, GeneratedAPIKey{}, err
	}
	generated, err := GenerateAPIKey(masterKey, alias, now)
	if err != nil {
		return Config{}, GeneratedAPIKey{}, err
	}
	updated, err := AddAPIKey(value, generated)
	if err != nil {
		return Config{}, GeneratedAPIKey{}, err
	}
	if err := s.Save(updated); err != nil {
		return Config{}, GeneratedAPIKey{}, err
	}
	return updated, generated, nil
}

func (s Store) RevokeAPIKey(value Config, id string, now time.Time) (Config, error) {
	updated, err := RevokeAPIKey(value, id, now)
	if err != nil {
		return Config{}, err
	}
	if err := s.Save(updated); err != nil {
		return Config{}, err
	}
	return updated, nil
}
