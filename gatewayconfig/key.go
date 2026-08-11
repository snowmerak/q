package gatewayconfig

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zeebo/blake3"
)

const apiKeyDomain = "q.gateway.api-key.v1\x00"

type GeneratedAPIKey struct {
	Record APIKey
	Secret string
}

func GenerateAPIKey(masterKey [32]byte, alias string, now time.Time) (GeneratedAPIKey, error) {
	alias = strings.TrimSpace(alias)
	if err := ValidateAlias(alias); err != nil {
		return GeneratedAPIKey{}, err
	}
	idBytes := make([]byte, 12)
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(idBytes); err != nil {
		return GeneratedAPIKey{}, fmt.Errorf("gatewayconfig: generate API key ID: %w", err)
	}
	if _, err := rand.Read(secretBytes); err != nil {
		return GeneratedAPIKey{}, fmt.Errorf("gatewayconfig: generate API key secret: %w", err)
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	secretPart := base64.RawURLEncoding.EncodeToString(secretBytes)
	secret := "qk_" + id + "." + secretPart
	record := APIKey{
		ID: id, Alias: alias, Hash: HashAPIKey(masterKey, id, secretPart),
		CreatedAt: now.UTC(),
	}
	return GeneratedAPIKey{Record: record, Secret: secret}, nil
}

func HashAPIKey(masterKey [32]byte, id, secret string) string {
	hasher, _ := blake3.NewKeyed(masterKey[:])
	_, _ = hasher.Write([]byte(apiKeyDomain))
	_, _ = hasher.Write([]byte(id))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(secret))
	return HashPrefix + base64.RawURLEncoding.EncodeToString(hasher.Sum(nil))
}

func VerifyAPIKey(masterKey [32]byte, record APIKey, presented string) bool {
	id, secret, ok := ParseAPIKey(presented)
	if !ok || id != record.ID || record.RevokedAt != nil {
		return false
	}
	want, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(record.Hash, HashPrefix))
	if err != nil || len(want) != 32 {
		return false
	}
	gotEncoded := HashAPIKey(masterKey, id, secret)
	got, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(gotEncoded, HashPrefix))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

func ParseAPIKey(value string) (string, string, bool) {
	if !strings.HasPrefix(value, "qk_") {
		return "", "", false
	}
	id, secret, found := strings.Cut(strings.TrimPrefix(value, "qk_"), ".")
	if !found || id == "" || secret == "" {
		return "", "", false
	}
	return id, secret, true
}

func AddAPIKey(value Config, generated GeneratedAPIKey) (Config, error) {
	for _, existing := range value.APIKeys {
		if strings.EqualFold(existing.Alias, generated.Record.Alias) {
			return Config{}, fmt.Errorf("gatewayconfig: API key alias %q already exists", generated.Record.Alias)
		}
	}
	value.APIKeys = append(value.APIKeys, generated.Record)
	if err := value.Validate(); err != nil {
		return Config{}, err
	}
	return value, nil
}

func RevokeAPIKey(value Config, id string, now time.Time) (Config, error) {
	for index := range value.APIKeys {
		if value.APIKeys[index].ID != id {
			continue
		}
		if value.APIKeys[index].RevokedAt != nil {
			return Config{}, errors.New("gatewayconfig: API key is already revoked")
		}
		revokedAt := now.UTC()
		value.APIKeys[index].RevokedAt = &revokedAt
		return value, value.Validate()
	}
	return Config{}, fmt.Errorf("gatewayconfig: API key %q was not found", id)
}

func (s Store) CreateAPIKey(value Config, alias string, now time.Time) (Config, GeneratedAPIKey, error) {
	var randomMaster [32]byte
	if _, err := rand.Read(randomMaster[:]); err != nil {
		return Config{}, GeneratedAPIKey{}, fmt.Errorf("gatewayconfig: generate master key: %w", err)
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
